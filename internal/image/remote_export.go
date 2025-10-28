package image

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/klauspost/pgzip"
	"github.com/so2liu/imgcd/internal/bundle"
	"github.com/so2liu/imgcd/internal/cache"
	remotedownload "github.com/so2liu/imgcd/internal/remote"
)

// RemoteExporter handles exporting images using blob-based caching
// This stores compressed blobs directly without decompression,
// significantly improving performance
type RemoteExporter struct {
	version        string
	blobCache      *cache.BlobCache
	blobDownloader *remotedownload.BlobDownloader
}

// NewRemoteExporter creates a new remote exporter
func NewRemoteExporter(version string, useCache bool) (*RemoteExporter, error) {
	blobCache, err := cache.NewBlobCache(useCache)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize blob cache: %w", err)
	}

	return &RemoteExporter{
		version:        version,
		blobCache:      blobCache,
		blobDownloader: remotedownload.NewBlobDownloader(blobCache),
	}, nil
}

// ExportFromRegistry exports an image directly from registry using blob caching
func (re *RemoteExporter) ExportFromRegistry(ctx context.Context, newRef, sinceRef, outDir string, opts ExportOptions) (string, error) {
	fmt.Printf("Using remote mode: downloading compressed blobs\n")
	fmt.Printf("Target platform: %s\n", opts.TargetPlatform)

	// Parse platform
	platform, err := v1.ParsePlatform(opts.TargetPlatform)
	if err != nil {
		return "", fmt.Errorf("failed to parse platform: %w", err)
	}

	// Fetch new image from registry
	fmt.Printf("Fetching image metadata for %s...\n", newRef)
	newImage, err := re.fetchImage(ctx, newRef, platform)
	if err != nil {
		return "", fmt.Errorf("failed to fetch new image: %w", err)
	}

	// Get manifest and config
	manifest, err := newImage.Manifest()
	if err != nil {
		return "", fmt.Errorf("failed to get manifest: %w", err)
	}

	configFile, err := newImage.ConfigFile()
	if err != nil {
		return "", fmt.Errorf("failed to get config file: %w", err)
	}

	// Get layers
	newLayers, err := newImage.Layers()
	if err != nil {
		return "", fmt.Errorf("failed to get layers: %w", err)
	}

	// Determine layers to export
	var layersToExport []v1.Layer
	var layerInfos []bundle.LayerInfo
	fullSinceRef := ""

	if sinceRef != "" {
		// Incremental export
		fullSinceRef = normalizeSinceRef(newRef, sinceRef)
		fmt.Printf("Calculating diff with: %s\n", fullSinceRef)

		baseImage, err := re.fetchImage(ctx, fullSinceRef, platform)
		if err != nil {
			return "", fmt.Errorf("failed to fetch base image: %w", err)
		}

		baseLayers, err := baseImage.Layers()
		if err != nil {
			return "", fmt.Errorf("failed to get base layers: %w", err)
		}

		// Build map of base layer DiffIDs
		baseDiffIDs := make(map[string]bool)
		for _, layer := range baseLayers {
			diffID, err := layer.DiffID()
			if err != nil {
				continue
			}
			baseDiffIDs[diffID.String()] = true
		}

		// Filter out shared layers
		fmt.Printf("Creating incremental export...\n")
		var filteredSize int64
		var totalSize int64

		for i, layer := range newLayers {
			diffID, err := layer.DiffID()
			if err != nil {
				return "", fmt.Errorf("failed to get layer DiffID: %w", err)
			}

			digest, err := layer.Digest()
			if err != nil {
				return "", fmt.Errorf("failed to get layer digest: %w", err)
			}

			size, _ := layer.Size()
			totalSize += size

			if baseDiffIDs[diffID.String()] {
				filteredSize += size
				continue
			}

			layersToExport = append(layersToExport, layer)

			// Build layer info
			mediaType := ""
			if i < len(manifest.Layers) {
				mediaType = string(manifest.Layers[i].MediaType)
			}

			layerInfos = append(layerInfos, bundle.LayerInfo{
				Digest:    digest.String(),
				DiffID:    diffID.String(),
				Size:      size,
				MediaType: mediaType,
			})
		}

		fmt.Printf("Filtered %d/%d layers (saved %.1f MB)\n",
			len(newLayers)-len(layersToExport), len(newLayers),
			float64(filteredSize)/(1024*1024))
	} else {
		// Full export
		fmt.Printf("Creating full export...\n")
		layersToExport = newLayers

		// Build layer infos for all layers
		for i, layer := range newLayers {
			diffID, err := layer.DiffID()
			if err != nil {
				return "", fmt.Errorf("failed to get layer DiffID: %w", err)
			}

			digest, err := layer.Digest()
			if err != nil {
				return "", fmt.Errorf("failed to get layer digest: %w", err)
			}

			size, _ := layer.Size()

			mediaType := ""
			if i < len(manifest.Layers) {
				mediaType = string(manifest.Layers[i].MediaType)
			}

			layerInfos = append(layerInfos, bundle.LayerInfo{
				Digest:    digest.String(),
				DiffID:    diffID.String(),
				Size:      size,
				MediaType: mediaType,
			})
		}
	}

	// Check if we have layers to export
	if len(layersToExport) == 0 {
		fmt.Printf("Warning: All layers already exist in base image. Creating minimal export.\n")
		layersToExport = newLayers
	}

	// Download blobs (this is the key optimization - no decompression!)
	fmt.Printf("\nDownloading %d layer(s)...\n", len(layersToExport))
	results, err := re.blobDownloader.DownloadBlobsWithProgress(
		ctx,
		layersToExport,
		newRef,
		4, // Max 4 concurrent downloads
		func(completed, total int, currentBlob string) {
			fmt.Fprintf(os.Stderr, "Progress: %d/%d blobs downloaded\r", completed, total)
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to download blobs: %w", err)
	}

	fmt.Printf("\nAll blobs downloaded/cached\n")

	// Count cache hits
	cacheHits := 0
	for _, result := range results {
		if result.FromCache {
			cacheHits++
		}
	}
	if cacheHits > 0 {
		fmt.Printf("Cache hits: %d/%d blobs\n", cacheHits, len(results))
	}

	// Create bundle metadata
	metadata := bundle.Metadata{
		Version:   "2",
		ImageRef:  newRef,
		BaseRef:   fullSinceRef,
		Platform:  opts.TargetPlatform,
		Manifest:  manifest,
		Config:    configFile,
		Layers:    layerInfos,
		TotalSize: calculateTotalSize(layerInfos),
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	// Create output directory
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate output paths
	repo, tag := parseReference(newRef)
	tarGzPath := generateFilename(repo, tag, sinceRef, outDir, true)

	// Create the bundle tar.gz
	fmt.Printf("\nPacking blobs into bundle...\n")
	if err := re.createBundleTarGz(tarGzPath, metadata, results); err != nil {
		return "", fmt.Errorf("failed to create bundle: %w", err)
	}

	// Create self-extracting script
	fmt.Printf("Creating self-extracting bundle for %s...\n", opts.TargetPlatform)
	bundlePath := generateFilename(repo, tag, sinceRef, outDir, false)

	bundleGen := NewBundleGenerator(re.version)
	if err := bundleGen.GenerateBundle(tarGzPath, bundlePath, opts.TargetPlatform, newRef); err != nil {
		return "", fmt.Errorf("failed to create self-extracting bundle: %w", err)
	}

	// Remove the intermediate tar.gz file
	os.Remove(tarGzPath)

	return bundlePath, nil
}

// createBundleTarGz creates a tar.gz bundle with metadata and compressed blobs
func (re *RemoteExporter) createBundleTarGz(outputPath string, metadata bundle.Metadata, downloadResults []remotedownload.DownloadResult) error {
	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Create pgzip writer for parallel compression
	gzw := pgzip.NewWriter(outFile)
	defer gzw.Close()

	// Create tar writer
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Write metadata.json
	metaBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "metadata.json",
		Mode: 0644,
		Size: int64(len(metaBytes)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(metaBytes); err != nil {
		return err
	}

	// Write each blob to the tar
	for i, result := range downloadResults {
		// Get blob from cache
		blobReader, err := re.blobDownloader.GetCachedBlobReader(result.Digest)
		if err != nil {
			return fmt.Errorf("failed to read blob %s from cache: %w", result.Digest, err)
		}
		defer blobReader.Close()

		// Get blob file info for size
		meta, err := re.blobCache.GetMetadata(result.Digest)
		if err != nil {
			return fmt.Errorf("failed to get blob metadata: %w", err)
		}

		// Write blob to tar as blobs/sha256/{hash}
		hash := strings.TrimPrefix(result.Digest, "sha256:")
		blobPath := filepath.Join("blobs", "sha256", hash)

		if err := tw.WriteHeader(&tar.Header{
			Name: blobPath,
			Mode: 0644,
			Size: meta.Size,
		}); err != nil {
			return err
		}

		// Copy blob content
		written, err := io.Copy(tw, blobReader)
		if err != nil {
			return fmt.Errorf("failed to write blob to tar: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Packed blob %d/%d (%s, %d bytes)\r", i+1, len(downloadResults), result.Digest[:19], written)
	}

	fmt.Fprintf(os.Stderr, "\nBundle created successfully\n")
	return nil
}

// fetchImage fetches an image from registry
func (re *RemoteExporter) fetchImage(ctx context.Context, imageRef string, platform *v1.Platform) (v1.Image, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithPlatform(*platform),
	}

	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image: %w", err)
	}

	return desc.Image()
}

// calculateTotalSize calculates the total compressed size of all layers
func calculateTotalSize(layers []bundle.LayerInfo) int64 {
	var total int64
	for _, layer := range layers {
		total += layer.Size
	}
	return total
}

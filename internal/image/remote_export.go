package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/so2liu/imgcd/internal/cache"
)

// RemoteExporter handles exporting images directly from registry without local runtime
type RemoteExporter struct {
	version    string
	layerCache *cache.LayerCache
}

// progressReader wraps an io.Reader and reports progress
type progressReader struct {
	reader      io.Reader
	total       int64
	current     int64
	layerID     string
	lastPrint   time.Time
	minInterval time.Duration
}

// newProgressReader creates a new progress reader
func newProgressReader(reader io.Reader, total int64, layerID string) *progressReader {
	return &progressReader{
		reader:      reader,
		total:       total,
		current:     0,
		layerID:     layerID,
		lastPrint:   time.Now(),
		minInterval: 100 * time.Millisecond, // Update at most every 100ms
	}
}

// Read implements io.Reader
func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		atomic.AddInt64(&pr.current, int64(n))

		// Only print if enough time has passed
		now := time.Now()
		if now.Sub(pr.lastPrint) >= pr.minInterval {
			pr.lastPrint = now
			pr.printProgress()
		}
	}

	// Print final progress on EOF
	if err == io.EOF {
		pr.printProgressComplete()
	}

	return n, err
}

// printProgress prints the current download progress
func (pr *progressReader) printProgress() {
	current := atomic.LoadInt64(&pr.current)
	percentage := float64(current) / float64(pr.total) * 100

	// Create progress bar (50 chars wide)
	barWidth := 50
	filled := int(percentage / 100 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("=", filled)
	if filled < barWidth {
		bar += ">"
		bar += strings.Repeat(" ", barWidth-filled-1)
	}

	// Format sizes
	currentSize := formatSize(current)
	totalSize := formatSize(pr.total)

	fmt.Fprintf(os.Stderr, "\r%s: Downloading [%s] %s/%s",
		pr.layerID, bar, currentSize, totalSize)
}

// printProgressComplete prints the completion message
func (pr *progressReader) printProgressComplete() {
	current := atomic.LoadInt64(&pr.current)
	size := formatSize(current)
	fmt.Fprintf(os.Stderr, "\r%s: Download complete (%s)\n", pr.layerID, size)
}

// formatSize formats bytes into human-readable size
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
	)

	switch {
	case bytes < KB:
		return fmt.Sprintf("%dB", bytes)
	case bytes < MB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/MB)
	}
}

// NewRemoteExporter creates a new remote exporter
func NewRemoteExporter(version string, useCache bool) (*RemoteExporter, error) {
	layerCache, err := cache.NewLayerCache(useCache)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize layer cache: %w", err)
	}

	return &RemoteExporter{
		version:    version,
		layerCache: layerCache,
	}, nil
}

// ExportFromRegistry exports an image directly from registry
func (re *RemoteExporter) ExportFromRegistry(ctx context.Context, newRef, sinceRef, outDir string, opts ExportOptions) (string, error) {
	fmt.Printf("Using remote mode: downloading directly from registry\n")
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

	// Get layers to export
	var layersToExport []v1.Layer
	var filteredSize int64
	var totalSize int64

	newLayers, err := newImage.Layers()
	if err != nil {
		return "", fmt.Errorf("failed to get layers: %w", err)
	}

	if sinceRef != "" {
		// Normalize since reference
		fullSinceRef := normalizeSinceRef(newRef, sinceRef)
		fmt.Printf("Calculating diff with: %s\n", fullSinceRef)

		// Fetch base image
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
		for _, layer := range newLayers {
			diffID, err := layer.DiffID()
			if err != nil {
				return "", fmt.Errorf("failed to get layer DiffID: %w", err)
			}

			size, _ := layer.Size()
			totalSize += size

			if baseDiffIDs[diffID.String()] {
				filteredSize += size
				continue
			}

			layersToExport = append(layersToExport, layer)
		}

		fmt.Printf("Filtered %d/%d layers (saved %.1f MB)\n",
			len(newLayers)-len(layersToExport), len(newLayers),
			float64(filteredSize)/(1024*1024))

		// Update sinceRef for metadata
		sinceRef = fullSinceRef
	} else {
		// Full export
		fmt.Printf("Creating full export...\n")
		layersToExport = newLayers
	}

	// Check if we have layers to export
	if len(layersToExport) == 0 {
		fmt.Printf("Warning: All layers already exist in base image. Creating minimal export.\n")
		layersToExport = newLayers // Export all layers as fallback
	}

	// Get config file
	configFile, err := newImage.ConfigFile()
	if err != nil {
		return "", fmt.Errorf("failed to get config file: %w", err)
	}

	// Create output directory
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate output paths
	repo, tag := parseReference(newRef)
	tarGzPath := generateFilename(repo, tag, sinceRef, outDir, true)

	// Create the tar.gz with image data
	if err := re.createRemoteTar(tarGzPath, newRef, sinceRef, configFile, layersToExport); err != nil {
		return "", fmt.Errorf("failed to create tar: %w", err)
	}

	// Create self-extracting bundle
	fmt.Printf("\nCreating self-extracting bundle for %s...\n", opts.TargetPlatform)
	bundlePath := generateFilename(repo, tag, sinceRef, outDir, false)

	bundleGen := NewBundleGenerator(re.version)
	if err := bundleGen.GenerateBundle(tarGzPath, bundlePath, opts.TargetPlatform, newRef); err != nil {
		return "", fmt.Errorf("failed to create bundle: %w", err)
	}

	// Remove the intermediate tar.gz file
	os.Remove(tarGzPath)

	return bundlePath, nil
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

// createRemoteTar creates a tar.gz containing the Docker image format
func (re *RemoteExporter) createRemoteTar(outputPath, newRef, sinceRef string, config *v1.ConfigFile, layers []v1.Layer) error {
	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Create gzip writer
	gzw := gzip.NewWriter(outFile)
	defer gzw.Close()

	// Create tar writer
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Write imgcd metadata
	incremental := sinceRef != ""
	meta := map[string]interface{}{
		"version":     "1.0",
		"new_ref":     newRef,
		"since_ref":   sinceRef,
		"incremental": incremental,
		"layer_count": len(layers),
		"export_mode": "remote",
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")

	if err := tw.WriteHeader(&tar.Header{
		Name: "imgcd-meta.json",
		Mode: 0644,
		Size: int64(len(metaBytes)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(metaBytes); err != nil {
		return err
	}

	// Create Docker image tar
	imageTar, err := re.createDockerImageTarFromRemote(config, layers, newRef)
	if err != nil {
		return fmt.Errorf("failed to create image tar: %w", err)
	}
	defer os.Remove(imageTar)

	// Add the image tar to our archive
	imageFile, err := os.Open(imageTar)
	if err != nil {
		return err
	}
	defer imageFile.Close()

	imageInfo, err := imageFile.Stat()
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "image.tar",
		Mode: 0644,
		Size: imageInfo.Size(),
	}); err != nil {
		return err
	}

	if _, err := io.Copy(tw, imageFile); err != nil {
		return err
	}

	return nil
}

// createDockerImageTarFromRemote creates a Docker format tar from remote layers
func (re *RemoteExporter) createDockerImageTarFromRemote(config *v1.ConfigFile, layers []v1.Layer, imageRef string) (string, error) {
	// Create temp file for the docker image tar
	tempFile, err := os.CreateTemp("", "imgcd-remote-*.tar")
	if err != nil {
		return "", err
	}
	tempPath := tempFile.Name()
	defer tempFile.Close()

	tw := tar.NewWriter(tempFile)
	defer tw.Close()

	// Write config file
	configHash := "unknown"
	if len(config.RootFS.DiffIDs) > 0 {
		configHash = strings.TrimPrefix(config.RootFS.DiffIDs[0].String(), "sha256:")[:12]
	}
	configName := configHash + ".json"

	configBytes, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: configName,
		Mode: 0644,
		Size: int64(len(configBytes)),
	}); err != nil {
		return "", err
	}
	if _, err := tw.Write(configBytes); err != nil {
		return "", err
	}

	// Write layers
	writtenLayerPaths := []string{}
	for _, layer := range layers {
		digest, _ := layer.Digest()
		diffID, _ := layer.DiffID()
		layerDir := strings.TrimPrefix(digest.String(), "sha256:")[:12]
		layerPath := layerDir + "/layer.tar"
		writtenLayerPaths = append(writtenLayerPaths, layerPath)

		// Get layer size
		size, err := layer.Size()
		if err != nil {
			return "", fmt.Errorf("failed to get layer size: %w", err)
		}

		// Create a temp file for the layer
		layerTemp, err := os.CreateTemp("", "layer-*.tar")
		if err != nil {
			return "", err
		}

		// Check cache first
		if re.layerCache.Exists(diffID.String()) {
			fmt.Fprintf(os.Stderr, "%s: Using cached layer\n", layerDir)

			cachedReader, err := re.layerCache.Get(diffID.String())
			if err == nil {
				// Copy from cache
				_, err = io.Copy(layerTemp, cachedReader)
				cachedReader.Close()
				layerTemp.Close()

				if err == nil {
					// Successfully used cache
					goto addToTar
				}
				// Cache read failed, fall through to download
			}
		}

		// Download layer (cache miss or cache read failed)
		{
			layerReader, err := layer.Uncompressed()
			if err != nil {
				os.Remove(layerTemp.Name())
				return "", fmt.Errorf("failed to get layer content: %w", err)
			}

			// Wrap with progress reader
			progressLayerReader := newProgressReader(layerReader, size, layerDir)

			// Use tee reader to write to both temp file and cache
			var cacheWriter io.Writer
			cacheTemp, err := os.CreateTemp("", "cache-*.tar.gz")
			if err == nil {
				cacheWriter = cacheTemp
				defer cacheTemp.Close()
				defer os.Remove(cacheTemp.Name())
			}

			var writer io.Writer = layerTemp
			if cacheWriter != nil {
				writer = io.MultiWriter(layerTemp, cacheWriter)
			}

			_, err = io.Copy(writer, progressLayerReader)
			layerReader.Close()
			layerTemp.Close()

			if err != nil {
				os.Remove(layerTemp.Name())
				return "", err
			}

			// Save to cache
			if cacheWriter != nil {
				cacheTemp.Close()
				cacheFile, err := os.Open(cacheTemp.Name())
				if err == nil {
					re.layerCache.Put(diffID.String(), cacheFile, imageRef, size)
					cacheFile.Close()
				}
			}
		}

	addToTar:

		// Add layer to tar
		layerFile, err := os.Open(layerTemp.Name())
		if err != nil {
			os.Remove(layerTemp.Name())
			return "", err
		}

		layerInfo, err := layerFile.Stat()
		if err != nil {
			layerFile.Close()
			os.Remove(layerTemp.Name())
			return "", err
		}

		if err := tw.WriteHeader(&tar.Header{
			Name: layerPath,
			Mode: 0644,
			Size: layerInfo.Size(),
		}); err != nil {
			layerFile.Close()
			os.Remove(layerTemp.Name())
			return "", err
		}

		if _, err := io.Copy(tw, layerFile); err != nil {
			layerFile.Close()
			os.Remove(layerTemp.Name())
			return "", err
		}

		layerFile.Close()
		os.Remove(layerTemp.Name())
	}

	// Write manifest.json
	manifest := []dockerManifest{
		{
			Config:   configName,
			RepoTags: []string{imageRef},
			Layers:   writtenLayerPaths,
		},
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0644,
		Size: int64(len(manifestBytes)),
	}); err != nil {
		return "", err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return "", err
	}

	// Write repositories file
	repo, tag := parseReference(imageRef)
	repositories := map[string]map[string]string{
		repo: {
			tag: strings.TrimPrefix(writtenLayerPaths[len(writtenLayerPaths)-1], "sha256:")[:12],
		},
	}

	repoBytes, err := json.Marshal(repositories)
	if err != nil {
		return "", err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "repositories",
		Mode: 0644,
		Size: int64(len(repoBytes)),
	}); err != nil {
		return "", err
	}
	if _, err := tw.Write(repoBytes); err != nil {
		return "", err
	}

	return tempPath, nil
}

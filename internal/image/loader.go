package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/so2liu/imgcd/internal/bundle"
	"github.com/so2liu/imgcd/internal/runtime"
)

// BundleLoader handles loading v2 bundles and reconstructing Docker images
type BundleLoader struct {
	runtime runtime.Runtime
}

// NewBundleLoader creates a new bundle loader
func NewBundleLoader(rt runtime.Runtime) *BundleLoader {
	return &BundleLoader{
		runtime: rt,
	}
}

// LoadBundle loads a v2 bundle and imports it into the container runtime
func (bl *BundleLoader) LoadBundle(ctx context.Context, bundlePath string) error {
	fmt.Printf("Loading bundle: %s\n", bundlePath)

	// Open bundle tar.gz
	bundleFile, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("failed to open bundle: %w", err)
	}
	defer bundleFile.Close()

	gzr, err := gzip.NewReader(bundleFile)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	// Read metadata first
	var metadata bundle.Metadata
	var blobsFound map[string]bool = make(map[string]bool)
	var tempDir string

	// Create temp directory for blobs
	tempDir, err = os.MkdirTemp("", "imgcd-load-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Extract bundle contents
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar: %w", err)
		}

		switch {
		case header.Name == "metadata.json":
			// Read metadata
			if err := json.NewDecoder(tr).Decode(&metadata); err != nil {
				return fmt.Errorf("failed to decode metadata: %w", err)
			}

			// Validate version
			if metadata.Version != "2" {
				return fmt.Errorf("unsupported bundle version: %s (expected 2)", metadata.Version)
			}

			fmt.Printf("Bundle version: %s\n", metadata.Version)
			fmt.Printf("Image: %s\n", metadata.ImageRef)
			fmt.Printf("Platform: %s\n", metadata.Platform)
			if metadata.BaseRef != "" {
				fmt.Printf("Base: %s\n", metadata.BaseRef)
			}

		case strings.HasPrefix(header.Name, "blobs/sha256/"):
			// Extract blob to temp directory
			hash := filepath.Base(header.Name)
			digest := "sha256:" + hash

			blobPath := filepath.Join(tempDir, hash)
			if err := bl.extractFile(tr, blobPath); err != nil {
				return fmt.Errorf("failed to extract blob %s: %w", digest, err)
			}

			blobsFound[digest] = true
		}
	}

	// Validate we have all required blobs
	fmt.Printf("\nValidating blobs...\n")
	for _, layerInfo := range metadata.Layers {
		if !blobsFound[layerInfo.Digest] {
			return fmt.Errorf("missing blob: %s", layerInfo.Digest)
		}
	}

	// Reconstruct Docker image.tar
	fmt.Printf("Reconstructing Docker image.tar...\n")
	imageTarPath := filepath.Join(tempDir, "image.tar")
	if err := bl.rebuildImageTar(imageTarPath, tempDir, &metadata); err != nil {
		return fmt.Errorf("failed to rebuild image.tar: %w", err)
	}

	// Load into runtime
	fmt.Printf("\nLoading image into container runtime...\n")
	imageTarFile, err := os.Open(imageTarPath)
	if err != nil {
		return fmt.Errorf("failed to open image.tar: %w", err)
	}
	defer imageTarFile.Close()

	if err := bl.runtime.LoadImageFromReader(ctx, imageTarFile); err != nil {
		return fmt.Errorf("failed to load image: %w", err)
	}

	fmt.Printf("Successfully loaded image: %s\n", metadata.ImageRef)
	return nil
}

// rebuildImageTar reconstructs a Docker-format image.tar from blobs
func (bl *BundleLoader) rebuildImageTar(outputPath, blobDir string, metadata *bundle.Metadata) error {
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	// Write config file
	configBytes, err := json.Marshal(metadata.Config)
	if err != nil {
		return err
	}

	// Generate config filename from first layer diffID
	configHash := "unknown"
	if len(metadata.Layers) > 0 {
		configHash = strings.TrimPrefix(metadata.Layers[0].DiffID, "sha256:")[:12]
	}
	configName := configHash + ".json"

	if err := tw.WriteHeader(&tar.Header{
		Name: configName,
		Mode: 0644,
		Size: int64(len(configBytes)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(configBytes); err != nil {
		return err
	}

	// Process each layer: decompress, verify, write to tar
	var writtenLayerPaths []string

	for i, layerInfo := range metadata.Layers {
		fmt.Printf("Processing layer %d/%d...\r", i+1, len(metadata.Layers))

		// Get blob path
		hash := strings.TrimPrefix(layerInfo.Digest, "sha256:")
		blobPath := filepath.Join(blobDir, hash)

		// Decompress and verify
		uncompressedLayer, calculatedDiffID, err := bl.decompressAndVerify(blobPath, layerInfo.DiffID)
		if err != nil {
			return fmt.Errorf("failed to decompress/verify layer %d: %w", i, err)
		}
		defer os.Remove(uncompressedLayer)

		if calculatedDiffID != layerInfo.DiffID {
			return fmt.Errorf("DiffID mismatch for layer %d: expected %s, got %s",
				i, layerInfo.DiffID, calculatedDiffID)
		}

		// Write layer to image.tar
		layerDir := strings.TrimPrefix(layerInfo.DiffID, "sha256:")[:12]
		layerPath := layerDir + "/layer.tar"
		writtenLayerPaths = append(writtenLayerPaths, layerPath)

		layerFile, err := os.Open(uncompressedLayer)
		if err != nil {
			return err
		}
		defer layerFile.Close()

		layerInfo, err := layerFile.Stat()
		if err != nil {
			return err
		}

		if err := tw.WriteHeader(&tar.Header{
			Name: layerPath,
			Mode: 0644,
			Size: layerInfo.Size(),
		}); err != nil {
			return err
		}

		if _, err := io.Copy(tw, layerFile); err != nil {
			return err
		}
	}

	fmt.Printf("\nAll layers processed\n")

	// Write manifest.json
	manifest := []dockerManifest{
		{
			Config:   configName,
			RepoTags: []string{metadata.ImageRef},
			Layers:   writtenLayerPaths,
		},
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0644,
		Size: int64(len(manifestBytes)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return err
	}

	// Write repositories file
	repo, tag := parseReference(metadata.ImageRef)
	repositories := map[string]map[string]string{
		repo: {
			tag: strings.TrimPrefix(writtenLayerPaths[len(writtenLayerPaths)-1], "sha256:")[:12],
		},
	}

	repoBytes, err := json.Marshal(repositories)
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "repositories",
		Mode: 0644,
		Size: int64(len(repoBytes)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(repoBytes); err != nil {
		return err
	}

	return nil
}

// decompressAndVerify decompresses a blob and verifies its DiffID
// Returns the path to the uncompressed layer tar and the calculated DiffID
func (bl *BundleLoader) decompressAndVerify(blobPath, expectedDiffID string) (string, string, error) {
	// Open compressed blob
	blobFile, err := os.Open(blobPath)
	if err != nil {
		return "", "", err
	}
	defer blobFile.Close()

	// Create gzip reader
	gzr, err := gzip.NewReader(blobFile)
	if err != nil {
		return "", "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	// Create temp file for uncompressed layer
	tempFile, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return "", "", err
	}
	defer tempFile.Close()

	// Decompress while calculating SHA256
	hasher := sha256.New()
	tee := io.TeeReader(gzr, hasher)

	if _, err := io.Copy(tempFile, tee); err != nil {
		os.Remove(tempFile.Name())
		return "", "", fmt.Errorf("failed to decompress: %w", err)
	}

	// Calculate DiffID
	calculatedDiffID := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	return tempFile.Name(), calculatedDiffID, nil
}

// extractFile extracts a file from tar to the specified path
func (bl *BundleLoader) extractFile(tr *tar.Reader, outputPath string) error {
	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	// Create file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Copy content
	if _, err := io.Copy(outFile, tr); err != nil {
		return err
	}

	return nil
}

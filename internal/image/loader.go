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

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/so2liu/imgcd/internal/bundle"
	"github.com/so2liu/imgcd/internal/runtime"
)

// BundleLoader handles loading bundles and reconstructing Docker images
type BundleLoader struct {
	runtime runtime.Runtime
}

// v1Metadata represents the metadata format from local mode (v1.0)
type v1Metadata struct {
	Version     string `json:"version"`
	NewRef      string `json:"new_ref"`
	SinceRef    string `json:"since_ref"`
	Incremental bool   `json:"incremental"`
	LayerCount  int    `json:"layer_count"`
}

// NewBundleLoader creates a new bundle loader
func NewBundleLoader(rt runtime.Runtime) *BundleLoader {
	return &BundleLoader{
		runtime: rt,
	}
}

// LoadBundle loads a bundle and imports it into the container runtime
// Supports both v1.0 (imgcd-meta.json + image.tar) and v2 (metadata.json + blobs) formats
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
	var v1Meta v1Metadata
	var blobsFound map[string]bool = make(map[string]bool)
	var tempDir string
	var isV1Format bool
	var imageTarPath string

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
		case header.Name == "imgcd-meta.json":
			// v1.0 format (local mode)
			if err := json.NewDecoder(tr).Decode(&v1Meta); err != nil {
				return fmt.Errorf("failed to decode v1 metadata: %w", err)
			}
			isV1Format = true
			fmt.Printf("Bundle version: %s (legacy format)\n", v1Meta.Version)
			fmt.Printf("Image: %s\n", v1Meta.NewRef)
			if v1Meta.SinceRef != "" {
				fmt.Printf("Base: %s\n", v1Meta.SinceRef)
			}

		case header.Name == "image.tar" && isV1Format:
			// v1.0 format: extract the nested image.tar
			imageTarPath = filepath.Join(tempDir, "image.tar")
			if err := bl.extractFile(tr, imageTarPath); err != nil {
				return fmt.Errorf("failed to extract image.tar: %w", err)
			}

		case header.Name == "metadata.json":
			// v2 format (remote mode)
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

	// Handle v1.0 format (legacy local mode)
	if isV1Format {
		return bl.loadV1Bundle(ctx, imageTarPath, v1Meta)
	}

	// Validate we have all required blobs
	fmt.Printf("\nValidating blobs...\n")
	for _, layerInfo := range metadata.Layers {
		if !blobsFound[layerInfo.Digest] {
			return fmt.Errorf("missing blob: %s", layerInfo.Digest)
		}
	}

	// For incremental imports, get base image info
	var baseImageDir string
	if metadata.BaseRef != "" {
		fmt.Printf("\nExporting base image from local runtime: %s\n", metadata.BaseRef)
		fmt.Printf("(This may take a while for large images...)\n")
		var err error
		baseImageDir, err = bl.extractBaseImage(ctx, metadata.BaseRef)
		if err != nil {
			return fmt.Errorf("incremental import requires base image %s: %w", metadata.BaseRef, err)
		}
		defer os.RemoveAll(baseImageDir)
		fmt.Printf("Base image exported successfully\n")
	}

	// Reconstruct Docker image.tar
	fmt.Printf("Reconstructing Docker image.tar...\n")
	imageTarPath = filepath.Join(tempDir, "image.tar")
	if err := bl.rebuildImageTar(imageTarPath, tempDir, &metadata, baseImageDir); err != nil {
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
// If baseImageDir is provided (incremental), merges base image layers with new layers
func (bl *BundleLoader) rebuildImageTar(outputPath, blobDir string, metadata *bundle.Metadata, baseImageDir string) error {
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	// Validate metadata.Config
	if metadata.Config == nil {
		return fmt.Errorf("metadata.Config is nil")
	}
	if len(metadata.Config.RootFS.DiffIDs) == 0 {
		return fmt.Errorf("metadata.Config.RootFS.DiffIDs is empty")
	}

	// Use metadata's full config (already contains all layers)
	mergedConfig := metadata.Config
	var writtenLayerPaths []string
	var totalLayers int

	if baseImageDir != "" && metadata.SharedLayerCount > 0 {
		// Incremental: copy shared layers from base, then add new layers
		_, baseLayers, err := bl.parseBaseImage(baseImageDir)
		if err != nil {
			return fmt.Errorf("failed to parse base image: %w", err)
		}

		// Validate we have enough base layers
		if metadata.SharedLayerCount > len(baseLayers) {
			return fmt.Errorf("base image has %d layers but need %d shared layers", len(baseLayers), metadata.SharedLayerCount)
		}

		// Copy first N layers from base image (shared layers)
		totalLayers = metadata.SharedLayerCount + len(metadata.Layers)
		for i := 0; i < metadata.SharedLayerCount; i++ {
			layerPath := baseLayers[i]
			fmt.Printf("Processing base layer %d/%d...\r", i+1, totalLayers)
			if err := bl.copyLayerToTar(tw, filepath.Join(baseImageDir, layerPath), layerPath); err != nil {
				return fmt.Errorf("failed to copy base layer: %w", err)
			}
			writtenLayerPaths = append(writtenLayerPaths, layerPath)
		}
	} else {
		// Full export: all layers from bundle
		totalLayers = len(metadata.Layers)
	}

	// Write merged config
	configBytes, err := json.Marshal(mergedConfig)
	if err != nil {
		return err
	}

	configHash := "unknown"
	if len(mergedConfig.RootFS.DiffIDs) > 0 {
		configHash = strings.TrimPrefix(mergedConfig.RootFS.DiffIDs[0].String(), "sha256:")[:12]
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

	// Process new layers from bundle
	baseLayerCount := len(writtenLayerPaths)
	for i, layerInfo := range metadata.Layers {
		fmt.Printf("Processing layer %d/%d...\r", baseLayerCount+i+1, totalLayers)

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
	if len(writtenLayerPaths) == 0 {
		return fmt.Errorf("no layers written to image.tar")
	}
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

// extractBaseImage exports the base image from runtime and extracts it to a temp directory
func (bl *BundleLoader) extractBaseImage(ctx context.Context, baseRef string) (string, error) {
	// Create temp directory for extracted base image
	tempDir, err := os.MkdirTemp("", "imgcd-base-*")
	if err != nil {
		return "", err
	}

	// Create temp file for base image tar
	baseTarFile, err := os.CreateTemp("", "base-*.tar")
	if err != nil {
		os.RemoveAll(tempDir)
		return "", err
	}
	baseTarPath := baseTarFile.Name()
	baseTarFile.Close()
	defer os.Remove(baseTarPath)

	// Save base image to tar
	if err := bl.runtime.SaveImage(ctx, baseRef, baseTarPath); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to save base image: %w", err)
	}

	// Extract base image tar
	baseTar, err := os.Open(baseTarPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", err
	}
	defer baseTar.Close()

	tr := tar.NewReader(baseTar)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(tempDir)
			return "", err
		}

		targetPath := filepath.Join(tempDir, header.Name)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				os.RemoveAll(tempDir)
				return "", err
			}
		} else {
			if err := bl.extractFile(tr, targetPath); err != nil {
				os.RemoveAll(tempDir)
				return "", err
			}
		}
	}

	return tempDir, nil
}

// parseBaseImage parses the extracted base image directory and returns config and layer paths
func (bl *BundleLoader) parseBaseImage(baseImageDir string) (*v1.ConfigFile, []string, error) {
	// Read manifest.json to get config and layers
	manifestPath := filepath.Join(baseImageDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read manifest.json: %w", err)
	}

	var manifests []dockerManifest
	if err := json.Unmarshal(manifestData, &manifests); err != nil {
		return nil, nil, fmt.Errorf("failed to parse manifest.json: %w", err)
	}

	if len(manifests) == 0 {
		return nil, nil, fmt.Errorf("no manifests found in base image")
	}

	manifest := manifests[0]

	// Read config file
	configPath := filepath.Join(baseImageDir, manifest.Config)
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config v1.ConfigFile
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, manifest.Layers, nil
}

// copyLayerToTar copies a layer file from source to the tar writer
func (bl *BundleLoader) copyLayerToTar(tw *tar.Writer, sourcePath, tarPath string) error {
	layerFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer layerFile.Close()

	info, err := layerFile.Stat()
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: tarPath,
		Mode: 0644,
		Size: info.Size(),
	}); err != nil {
		return err
	}

	if _, err := io.Copy(tw, layerFile); err != nil {
		return err
	}

	return nil
}

// loadV1Bundle handles the legacy v1.0 format (local mode)
// For non-incremental: image.tar can be loaded directly
// For incremental: need to merge base image layers with new layers
func (bl *BundleLoader) loadV1Bundle(ctx context.Context, imageTarPath string, meta v1Metadata) error {
	if imageTarPath == "" {
		return fmt.Errorf("image.tar not found in v1 bundle")
	}

	// Non-incremental: load directly
	if !meta.Incremental || meta.SinceRef == "" {
		fmt.Printf("\nLoading v1.0 format bundle (Docker-format image.tar)...\n")

		imageTarFile, err := os.Open(imageTarPath)
		if err != nil {
			return fmt.Errorf("failed to open image.tar: %w", err)
		}
		defer imageTarFile.Close()

		if err := bl.runtime.LoadImageFromReader(ctx, imageTarFile); err != nil {
			return fmt.Errorf("failed to load image: %w", err)
		}

		fmt.Printf("Successfully loaded image: %s\n", meta.NewRef)
		return nil
	}

	// Incremental: need to merge base image layers with new layers
	fmt.Printf("\nLoading v1.0 incremental format bundle...\n")
	fmt.Printf("This requires merging layers from base image: %s\n", meta.SinceRef)

	// Export base image to temp directory
	fmt.Printf("Exporting base image from local runtime...\n")
	fmt.Printf("(This may take a while for large images...)\n")
	baseImageDir, err := bl.extractBaseImage(ctx, meta.SinceRef)
	if err != nil {
		return fmt.Errorf("incremental import requires base image %s: %w", meta.SinceRef, err)
	}
	defer os.RemoveAll(baseImageDir)
	fmt.Printf("Base image exported successfully\n")

	// Extract new image.tar to temp directory
	newImageDir, err := os.MkdirTemp("", "imgcd-new-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(newImageDir)

	if err := bl.extractTarToDir(imageTarPath, newImageDir); err != nil {
		return fmt.Errorf("failed to extract new image: %w", err)
	}

	// Merge and rebuild
	fmt.Printf("Merging base and new layers...\n")
	mergedTarPath := filepath.Join(newImageDir, "merged.tar")
	if err := bl.mergeV1Layers(mergedTarPath, baseImageDir, newImageDir, meta.NewRef); err != nil {
		return fmt.Errorf("failed to merge layers: %w", err)
	}

	// Load merged image
	fmt.Printf("Loading merged image into container runtime...\n")
	mergedFile, err := os.Open(mergedTarPath)
	if err != nil {
		return fmt.Errorf("failed to open merged image: %w", err)
	}
	defer mergedFile.Close()

	if err := bl.runtime.LoadImageFromReader(ctx, mergedFile); err != nil {
		return fmt.Errorf("failed to load image: %w", err)
	}

	fmt.Printf("Successfully loaded image: %s\n", meta.NewRef)
	return nil
}

// extractTarToDir extracts a tar file to a directory
func (bl *BundleLoader) extractTarToDir(tarPath, destDir string) error {
	tarFile, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	tr := tar.NewReader(tarFile)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(destDir, header.Name)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		} else {
			// Create parent directory
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			if err := bl.extractFile(tr, targetPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeV1Layers merges base image layers with new image layers for v1.0 incremental format
func (bl *BundleLoader) mergeV1Layers(outputPath, baseDir, newDir, imageRef string) error {
	// Parse base image manifest and config
	_, baseLayers, err := bl.parseBaseImage(baseDir)
	if err != nil {
		return fmt.Errorf("failed to parse base image: %w", err)
	}

	// Parse new image manifest and config
	newConfig, newLayers, err := bl.parseBaseImage(newDir)
	if err != nil {
		return fmt.Errorf("failed to parse new image: %w", err)
	}

	// The new image config has all DiffIDs, but the tar only has the new layers
	// We need to figure out how many layers to copy from base
	sharedLayerCount := len(newConfig.RootFS.DiffIDs) - len(newLayers)
	if sharedLayerCount < 0 {
		sharedLayerCount = 0
	}

	fmt.Printf("Merging %d base layers + %d new layers = %d total layers\n",
		sharedLayerCount, len(newLayers), len(newConfig.RootFS.DiffIDs))

	// Create output tar
	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	var allLayerPaths []string

	// Copy shared layers from base image
	for i := 0; i < sharedLayerCount && i < len(baseLayers); i++ {
		layerPath := baseLayers[i]
		sourcePath := filepath.Join(baseDir, layerPath)
		if err := bl.copyLayerToTar(tw, sourcePath, layerPath); err != nil {
			return fmt.Errorf("failed to copy base layer %d: %w", i, err)
		}
		allLayerPaths = append(allLayerPaths, layerPath)
	}

	// Copy new layers
	for _, layerPath := range newLayers {
		sourcePath := filepath.Join(newDir, layerPath)
		if err := bl.copyLayerToTar(tw, sourcePath, layerPath); err != nil {
			return fmt.Errorf("failed to copy new layer: %w", err)
		}
		allLayerPaths = append(allLayerPaths, layerPath)
	}

	// Write config (use new image's config as it has all DiffIDs)
	configBytes, err := json.Marshal(newConfig)
	if err != nil {
		return err
	}

	configHash := "unknown"
	if len(newConfig.RootFS.DiffIDs) > 0 {
		configHash = strings.TrimPrefix(newConfig.RootFS.DiffIDs[0].String(), "sha256:")[:12]
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

	// Write manifest
	manifest := []dockerManifest{
		{
			Config:   configName,
			RepoTags: []string{imageRef},
			Layers:   allLayerPaths,
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
	repo, tag := parseReference(imageRef)
	if len(allLayerPaths) == 0 {
		return fmt.Errorf("no layers to write")
	}
	repositories := map[string]map[string]string{
		repo: {
			tag: strings.TrimPrefix(allLayerPaths[len(allLayerPaths)-1], "sha256:")[:12],
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

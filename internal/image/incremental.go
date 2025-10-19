package image

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// dockerManifest represents the manifest.json in docker save tar
type dockerManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// createIncrementalExportV2 creates a real incremental export by filtering layers
func (e *Exporter) createIncrementalExportV2(inputPath, outputPath, newRef, sinceRef string, oldLayerDigests map[string]bool) (string, error) {
	// Parse the docker save tar to extract layers
	img, err := tarball.ImageFromPath(inputPath, nil)
	if err != nil {
		return "", fmt.Errorf("failed to parse image tar: %w", err)
	}

	// Get layers from the new image
	layers, err := img.Layers()
	if err != nil {
		return "", fmt.Errorf("failed to get layers: %w", err)
	}

	// Get config
	configFile, err := img.ConfigFile()
	if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}

	// Filter out old layers
	newLayers := []v1.Layer{}
	newLayerPaths := []string{}
	totalSize := int64(0)
	filteredSize := int64(0)

	for i, layer := range layers {
		// Get DiffID (uncompressed digest) to match docker inspect format
		diffID, err := layer.DiffID()
		if err != nil {
			return "", fmt.Errorf("failed to get layer DiffID: %w", err)
		}

		size, err := layer.Size()
		if err != nil {
			size = 0
		}
		totalSize += size

		// Check if this layer exists in old image (using DiffID)
		if oldLayerDigests[diffID.String()] {
			filteredSize += size
			continue
		}

		newLayers = append(newLayers, layer)
		newLayerPaths = append(newLayerPaths, fmt.Sprintf("layer-%d.tar", i))
	}

	fmt.Printf("Filtered %d/%d layers (saved %.1f MB)\n",
		len(layers)-len(newLayers), len(layers),
		float64(filteredSize)/(1024*1024))

	// If all layers are filtered, we still need to export something
	if len(newLayers) == 0 {
		fmt.Printf("Warning: All layers already exist in base image. Creating minimal export.\n")
		// Fall back to full export in this case
		return e.compressImage(inputPath, outputPath, newRef, sinceRef)
	}

	// Create the incremental tar.gz
	return e.createIncrementalTar(outputPath, newRef, sinceRef, configFile, newLayers, newLayerPaths)
}

func (e *Exporter) createIncrementalTar(outputPath, newRef, sinceRef string, config *v1.ConfigFile, layers []v1.Layer, layerPaths []string) (string, error) {
	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	// Create gzip writer
	gzw := gzip.NewWriter(outFile)
	defer gzw.Close()

	// Create tar writer
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Write imgcd metadata
	meta := map[string]interface{}{
		"version":     "1.0",
		"new_ref":     newRef,
		"since_ref":   sinceRef,
		"incremental": true,
		"layer_count": len(layers),
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")

	if err := tw.WriteHeader(&tar.Header{
		Name: "imgcd-meta.json",
		Mode: 0644,
		Size: int64(len(metaBytes)),
	}); err != nil {
		return "", err
	}
	if _, err := tw.Write(metaBytes); err != nil {
		return "", err
	}

	// Now create a nested tar for the docker image format
	// We need to create: manifest.json, config.json, and layer tars
	imageTar, err := e.createDockerImageTar(config, layers, layerPaths, newRef)
	if err != nil {
		return "", fmt.Errorf("failed to create image tar: %w", err)
	}
	defer os.Remove(imageTar)

	// Add the image tar to our archive
	imageFile, err := os.Open(imageTar)
	if err != nil {
		return "", err
	}
	defer imageFile.Close()

	imageInfo, err := imageFile.Stat()
	if err != nil {
		return "", err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: "image.tar",
		Mode: 0644,
		Size: imageInfo.Size(),
	}); err != nil {
		return "", err
	}

	if _, err := io.Copy(tw, imageFile); err != nil {
		return "", err
	}

	return outputPath, nil
}

func (e *Exporter) createDockerImageTar(config *v1.ConfigFile, layers []v1.Layer, layerPaths []string, imageRef string) (string, error) {
	// Create temp file for the docker image tar
	tempFile, err := os.CreateTemp("", "imgcd-image-*.tar")
	if err != nil {
		return "", err
	}
	tempPath := tempFile.Name()
	defer tempFile.Close()

	tw := tar.NewWriter(tempFile)
	defer tw.Close()

	// Write config file
	configHash := fmt.Sprintf("%x", config.RootFS.DiffIDs[0]) // Simplified - use first layer hash
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
		layerDir := strings.TrimPrefix(digest.String(), "sha256:")[:12]
		layerPath := layerDir + "/layer.tar"
		writtenLayerPaths = append(writtenLayerPaths, layerPath)

		// Get layer content
		rc, err := layer.Compressed()
		if err != nil {
			return "", err
		}

		// We need to get the uncompressed layer for docker format
		layerReader, err := layer.Uncompressed()
		if err != nil {
			rc.Close()
			return "", err
		}

		// Create a temp file for the layer
		layerTemp, err := os.CreateTemp("", "layer-*.tar")
		if err != nil {
			layerReader.Close()
			rc.Close()
			return "", err
		}
		_, err = io.Copy(layerTemp, layerReader)
		layerReader.Close()
		rc.Close()
		layerTemp.Close()

		if err != nil {
			os.Remove(layerTemp.Name())
			return "", err
		}

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

	// Write repositories file (optional but docker expects it)
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

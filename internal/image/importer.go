package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/so2liu/imgcd/internal/bundle"
	"github.com/so2liu/imgcd/internal/runtime"
)

// Importer imports container images from tar.gz archives
type Importer struct {
	runtime runtime.Runtime
}

// NewImporter creates a new image importer
func NewImporter() (*Importer, error) {
	rt, err := runtime.DetectRuntime()
	if err != nil {
		return nil, fmt.Errorf("failed to detect runtime: %w", err)
	}

	return &Importer{runtime: rt}, nil
}

// Import imports an image from a tar.gz file
func (i *Importer) Import(ctx context.Context, archivePath string) (string, error) {
	fmt.Printf("Using runtime: %s\n", i.runtime.Name())
	fmt.Printf("Loading bundle: %s\n", archivePath)

	// Load bundle using BundleLoader
	loader := NewBundleLoader(i.runtime)
	if err := loader.LoadBundle(ctx, archivePath); err != nil {
		return "", err
	}

	// Extract image name from bundle metadata
	imageName, err := i.extractImageName(archivePath)
	if err != nil {
		return "", err
	}

	return imageName, nil
}

// extractImageName reads the metadata to get the image name
func (i *Importer) extractImageName(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if header.Name == "metadata.json" {
			var meta bundle.Metadata
			if err := json.NewDecoder(tr).Decode(&meta); err != nil {
				return "", err
			}
			return meta.ImageRef, nil
		}
	}

	return "", fmt.Errorf("metadata.json not found in bundle")
}

// Close closes the importer
func (i *Importer) Close() error {
	return i.runtime.Close()
}

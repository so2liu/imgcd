package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

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

	// Open archive
	fmt.Printf("Opening archive: %s\n", archivePath)
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to open archive: %w", err)
	}
	defer f.Close()

	// Create gzip reader
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	// Create tar reader
	tr := tar.NewReader(gzr)

	// Extract metadata and image tar
	var metadata map[string]string
	var imageTarPath string

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read tar: %w", err)
		}

		switch header.Name {
		case "imgcd-meta.json":
			// Read metadata
			if err := json.NewDecoder(tr).Decode(&metadata); err != nil {
				return "", fmt.Errorf("failed to decode metadata: %w", err)
			}

		case "image.tar":
			// Extract image tar to temp file
			tempFile, err := os.CreateTemp("", "imgcd-image-*.tar")
			if err != nil {
				return "", fmt.Errorf("failed to create temp file: %w", err)
			}
			imageTarPath = tempFile.Name()

			if _, err := io.Copy(tempFile, tr); err != nil {
				tempFile.Close()
				os.Remove(imageTarPath)
				return "", fmt.Errorf("failed to extract image.tar: %w", err)
			}
			tempFile.Close()
		}
	}

	if imageTarPath == "" {
		return "", fmt.Errorf("image.tar not found in archive")
	}
	defer os.Remove(imageTarPath)

	// Load the image using the runtime
	fmt.Printf("Loading image into %s...\n", i.runtime.Name())
	if err := i.runtime.LoadImage(ctx, imageTarPath); err != nil {
		return "", fmt.Errorf("failed to load image: %w", err)
	}

	imageName := metadata["new_ref"]
	if imageName == "" {
		imageName = "unknown"
	}

	return imageName, nil
}

// Close closes the importer
func (i *Importer) Close() error {
	return i.runtime.Close()
}

package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/so2liu/imgcd/internal/runtime"
)

// Exporter exports container images to tar.gz archives
type Exporter struct {
	runtime runtime.Runtime
}

// NewExporter creates a new image exporter
func NewExporter() (*Exporter, error) {
	rt, err := runtime.DetectRuntime()
	if err != nil {
		return nil, fmt.Errorf("failed to detect runtime: %w", err)
	}

	return &Exporter{runtime: rt}, nil
}

// Export exports an image to a tar.gz file
func (e *Exporter) Export(ctx context.Context, newRef, sinceRef, outDir string) (string, error) {
	fmt.Printf("Using runtime: %s\n", e.runtime.Name())

	// Check and pull the new image if necessary
	fmt.Printf("Checking image %s...\n", newRef)
	if _, err := e.runtime.GetImage(ctx, newRef); err != nil {
		return "", fmt.Errorf("failed to get image %s: %w", newRef, err)
	}

	// Get old image layers if doing incremental export
	var oldLayers map[string]bool
	if sinceRef != "" {
		// If sinceRef is just a tag (no repo), use the same repo as newRef
		fullSinceRef := normalizeSinceRef(newRef, sinceRef)
		fmt.Printf("Calculating diff with: %s\n", fullSinceRef)

		oldImage, err := e.runtime.GetImage(ctx, fullSinceRef)
		if err != nil {
			return "", fmt.Errorf("failed to get base image %s: %w", fullSinceRef, err)
		}

		oldLayers = make(map[string]bool)
		for _, layer := range oldImage.Layers {
			oldLayers[layer.Digest] = true
		}

		// Use fullSinceRef for metadata
		sinceRef = fullSinceRef
	}

	// Save the new image to a temp file
	tempFile, err := os.CreateTemp("", "imgcd-*.tar")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	fmt.Printf("Saving image %s...\n", newRef)
	if err := e.runtime.SaveImage(ctx, newRef, tempFile.Name()); err != nil {
		return "", fmt.Errorf("failed to save image: %w", err)
	}

	// Create output file
	repo, tag := parseReference(newRef)
	outputPath := generateFilename(repo, tag, sinceRef, outDir)

	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// If no incremental export, just compress the tar
	if oldLayers == nil {
		fmt.Printf("Creating full export...\n")
		return e.compressImage(tempFile.Name(), outputPath, newRef, sinceRef)
	}

	// Otherwise, filter layers and create incremental export
	fmt.Printf("Creating incremental export...\n")
	return e.createIncrementalExport(tempFile.Name(), outputPath, newRef, sinceRef, oldLayers)
}

func (e *Exporter) compressImage(inputPath, outputPath, newRef, sinceRef string) (string, error) {
	// Open input file
	inFile, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer inFile.Close()

	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	// Create gzip writer
	gzw := gzip.NewWriter(outFile)
	defer gzw.Close()

	// Create tar writer for metadata
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Add metadata
	meta := map[string]string{
		"version":   "1.0",
		"new_ref":   newRef,
		"since_ref": sinceRef,
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

	// Copy the original tar into our tar
	if err := tw.WriteHeader(&tar.Header{
		Name: "image.tar",
		Mode: 0644,
		Size: getFileSize(inputPath),
	}); err != nil {
		return "", err
	}

	if _, err := io.Copy(tw, inFile); err != nil {
		return "", err
	}

	return outputPath, nil
}

func (e *Exporter) createIncrementalExport(inputPath, outputPath, newRef, sinceRef string, oldLayers map[string]bool) (string, error) {
	// Use the new v2 implementation for real incremental export
	return e.createIncrementalExportV2(inputPath, outputPath, newRef, sinceRef, oldLayers)
}

func parseReference(ref string) (repo, tag string) {
	parts := strings.Split(ref, ":")
	if len(parts) >= 2 {
		return strings.Join(parts[:len(parts)-1], ":"), parts[len(parts)-1]
	}
	return ref, "latest"
}

// normalizeSinceRef converts a short tag to a full image reference
// If sinceRef is just a tag (e.g., "3.19"), it uses the repository from newRef
// If sinceRef is a full reference (e.g., "alpine:3.19"), it returns as-is
func normalizeSinceRef(newRef, sinceRef string) string {
	// Check if sinceRef looks like a full image reference (contains / or :)
	if strings.Contains(sinceRef, "/") || strings.Contains(sinceRef, ":") {
		return sinceRef
	}

	// sinceRef is just a tag, extract repo from newRef and combine
	repo, _ := parseReference(newRef)
	return fmt.Sprintf("%s:%s", repo, sinceRef)
}

func generateFilename(repo, tag, sinceRef, outDir string) string {
	// Clean repository name (replace / and : with _)
	cleanRepo := strings.ReplaceAll(repo, "/", "_")
	cleanRepo = strings.ReplaceAll(cleanRepo, ":", "_")

	// Determine since tag
	sinceTag := "none"
	if sinceRef != "" {
		_, sinceTag = parseReference(sinceRef)
	}

	filename := fmt.Sprintf("%s-%s__since-%s.tar.gz", cleanRepo, tag, sinceTag)
	return filepath.Join(outDir, filename)
}

func getFileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Close closes the exporter
func (e *Exporter) Close() error {
	return e.runtime.Close()
}

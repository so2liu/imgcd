package image

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// BundleGenerator generates tar bundles containing imgcd binary and image data
type BundleGenerator struct {
	version string
}

// NewBundleGenerator creates a new bundle generator
func NewBundleGenerator(version string) *BundleGenerator {
	return &BundleGenerator{
		version: version,
	}
}

// GenerateBundle creates a tar bundle containing imgcd binary and image data
func (bg *BundleGenerator) GenerateBundle(imageTarGzPath, outputPath, targetPlatform, imageName string) error {
	fmt.Printf("Creating bundle...\n")

	// Get imgcd binary for target platform
	binaryPath, err := bg.getOrDownloadBinary(targetPlatform)
	if err != nil {
		return fmt.Errorf("failed to get imgcd binary: %w", err)
	}

	// Create output tar file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Create tar writer
	tw := tar.NewWriter(outFile)
	defer tw.Close()

	// Add imgcd binary
	fmt.Printf("Adding imgcd binary...\n")
	if err := addFileToTar(tw, binaryPath, "imgcd", 0755); err != nil {
		return fmt.Errorf("failed to add imgcd binary: %w", err)
	}

	// Add image tar.gz
	fmt.Printf("Adding image data...\n")
	if err := addFileToTar(tw, imageTarGzPath, "image.tar.gz", 0644); err != nil {
		return fmt.Errorf("failed to add image data: %w", err)
	}

	// Get final size
	finalInfo, err := outFile.Stat()
	if err == nil {
		sizeMB := float64(finalInfo.Size()) / (1024 * 1024)
		fmt.Printf("Bundle created successfully (%.1f MB)\n", sizeMB)
	}

	return nil
}

// addFileToTar adds a file to a tar archive
func addFileToTar(tw *tar.Writer, filePath, tarPath string, mode int64) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name: tarPath,
		Mode: mode,
		Size: info.Size(),
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

// getOrDownloadBinary gets the imgcd binary for the specified platform
// It first checks the cache, and downloads if not found
func (bg *BundleGenerator) getOrDownloadBinary(platform string) (string, error) {
	// In development mode (version == "dev"), use the current binary
	if bg.version == "dev" {
		return bg.useCurrentBinary(platform)
	}

	// Get cache directory
	cacheDir := bg.getCacheDir()
	binaryPath := filepath.Join(cacheDir, bg.version, platform, "imgcd")

	// Check if binary exists in cache
	if _, err := os.Stat(binaryPath); err == nil {
		fmt.Printf("Using cached imgcd binary for %s\n", platform)
		return binaryPath, nil
	}

	// Download binary
	fmt.Printf("Downloading imgcd binary for %s (version %s)...\n", platform, bg.version)
	if err := bg.downloadBinary(platform, binaryPath); err != nil {
		return "", err
	}

	return binaryPath, nil
}

// useCurrentBinary uses the current imgcd binary for development mode
func (bg *BundleGenerator) useCurrentBinary(platform string) (string, error) {
	// Check if user provided a pre-built binary for the target platform
	if customPath := os.Getenv("IMGCD_BINARY_PATH"); customPath != "" {
		if _, err := os.Stat(customPath); err != nil {
			return "", fmt.Errorf("custom binary not found at %s: %w", customPath, err)
		}
		fmt.Printf("Development mode: using custom binary from IMGCD_BINARY_PATH\n")
		return customPath, nil
	}

	// In dev mode, use current binary regardless of platform
	// This is for development convenience - the bundle may not work on different platforms
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	currentPlatform := detectCurrentPlatform()
	if currentPlatform != platform {
		fmt.Printf("Development mode: using current binary (%s) for target platform (%s)\n", currentPlatform, platform)
		fmt.Printf("Warning: This bundle will only work on %s systems\n", currentPlatform)
	} else {
		fmt.Printf("Development mode: using current platform binary (%s)\n", currentPlatform)
	}

	return execPath, nil
}

// detectCurrentPlatform detects the current OS and architecture
func detectCurrentPlatform() string {
	return fmt.Sprintf("%s/%s", goruntime.GOOS, goruntime.GOARCH)
}

// getPlatformOS extracts OS from platform string
func getPlatformOS(platform string) string {
	parts := strings.Split(platform, "/")
	if len(parts) == 2 {
		return parts[0]
	}
	return "linux"
}

// getPlatformArch extracts architecture from platform string
func getPlatformArch(platform string) string {
	parts := strings.Split(platform, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return "amd64"
}

// downloadBinary downloads the imgcd binary from GitHub releases
func (bg *BundleGenerator) downloadBinary(platform, outputPath string) error {
	// Parse platform (e.g., "linux/amd64" -> "linux-amd64")
	parts := strings.Split(platform, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid platform format: %s", platform)
	}
	osName := parts[0]
	arch := parts[1]

	// Construct download URL
	// Format: https://github.com/so2liu/imgcd/releases/download/v1.0.0/imgcd-linux-amd64.tar.gz
	filename := fmt.Sprintf("imgcd-%s-%s.tar.gz", osName, arch)

	// Ensure version has v prefix (but not vv)
	version := bg.version
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	url := fmt.Sprintf("https://github.com/so2liu/imgcd/releases/download/%s/%s", version, filename)

	// Create temporary directory for download
	tempDir, err := os.MkdirTemp("", "imgcd-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Download tar.gz
	tarGzPath := filepath.Join(tempDir, filename)
	if err := downloadFile(url, tarGzPath); err != nil {
		return fmt.Errorf("failed to download binary from %s: %w", url, err)
	}

	// Extract binary from tar.gz
	binaryName := fmt.Sprintf("imgcd-%s-%s", osName, arch)
	if err := extractBinaryFromTarGz(tarGzPath, binaryName, outputPath); err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	fmt.Printf("Binary downloaded and cached successfully\n")
	return nil
}

// getCacheDir returns the cache directory for imgcd binaries
func (bg *BundleGenerator) getCacheDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	return filepath.Join(homeDir, ".imgcd", "bin")
}

// downloadFile downloads a file from a URL
func downloadFile(url, filepath string) error {
	// Create directory
	if err := os.MkdirAll(filepath[:strings.LastIndex(filepath, "/")], 0755); err != nil {
		return err
	}

	// Download
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %s", resp.Status)
	}

	// Create file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write data
	_, err = io.Copy(out, resp.Body)
	return err
}

// extractBinaryFromTarGz extracts a binary from a tar.gz archive
func extractBinaryFromTarGz(tarGzPath, binaryName, outputPath string) error {
	// Create directory for output
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	// Open tar.gz file
	tarGzFile, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer tarGzFile.Close()

	// Create gzip reader
	gzr, err := gzipNewReader(tarGzPath)
	if err != nil {
		return err
	}
	defer gzr.Close()

	// Create tar reader
	tr := tarNewReader(gzr)

	// Find and extract the binary
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if header.Name == binaryName {
			// Extract binary
			outFile, err := os.Create(outputPath)
			if err != nil {
				return err
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tr); err != nil {
				return err
			}

			// Make executable
			if err := os.Chmod(outputPath, 0755); err != nil {
				return err
			}

			return nil
		}
	}

	return fmt.Errorf("binary %s not found in archive", binaryName)
}

// Helper functions for tar.gz extraction
func gzipNewReader(path string) (*gzip.Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return gzip.NewReader(file)
}

func tarNewReader(r io.Reader) *tar.Reader {
	return tar.NewReader(r)
}

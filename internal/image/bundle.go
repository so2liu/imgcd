package image

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// BundleGenerator generates self-extracting bundle scripts
type BundleGenerator struct {
	version string
}

// NewBundleGenerator creates a new bundle generator
func NewBundleGenerator(version string) *BundleGenerator {
	return &BundleGenerator{
		version: version,
	}
}

// GenerateBundle creates a self-extracting shell script bundle
func (bg *BundleGenerator) GenerateBundle(imageTarGzPath, outputPath, targetPlatform, imageName string) error {
	// Get imgcd binary for target platform
	binaryPath, err := bg.getOrDownloadBinary(targetPlatform)
	if err != nil {
		return fmt.Errorf("failed to get imgcd binary: %w", err)
	}

	// Read template
	templatePath := filepath.Join(getProjectRoot(), "templates", "self-extractor.sh")
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read template: %w", err)
	}

	// Read and encode binary
	binaryData, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("failed to read binary: %w", err)
	}
	binaryBase64 := base64.StdEncoding.EncodeToString(binaryData)

	// Read and encode image data
	imageData, err := os.ReadFile(imageTarGzPath)
	if err != nil {
		return fmt.Errorf("failed to read image data: %w", err)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	// Replace placeholders
	content := string(templateContent)
	content = strings.ReplaceAll(content, "{{TARGET_PLATFORM}}", targetPlatform)
	content = strings.ReplaceAll(content, "{{IMAGE_NAME}}", imageName)
	content = strings.ReplaceAll(content, "{{IMGCD_VERSION}}", bg.version)
	content = strings.ReplaceAll(content, "{{IMGCD_BINARY_BASE64}}", binaryBase64)
	content = strings.ReplaceAll(content, "{{IMAGE_DATA_BASE64}}", imageBase64)

	// Write output file
	if err := os.WriteFile(outputPath, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	return nil
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
		fmt.Printf("Development mode: using custom binary for %s from IMGCD_BINARY_PATH\n", platform)
		return customPath, nil
	}

	// Detect current platform
	currentPlatform := detectCurrentPlatform()

	// Check if platforms match
	if currentPlatform != platform {
		return "", fmt.Errorf(`Development mode: cannot create bundle for %s on %s

The embedded binary must match the target platform (%s), but the current
binary is for %s. In dev mode, imgcd cannot download binaries from GitHub.

Solutions:
  1. Cross-compile for the target platform and use IMGCD_BINARY_PATH:
     GOOS=%s GOARCH=%s go build -o imgcd-%s ./cmd/imgcd
     IMGCD_BINARY_PATH=./imgcd-%s ./imgcd save alpine

  2. Change target platform to match current platform:
     ./imgcd save alpine -t %s

  3. Build and test on the target platform directly
     (recommended for production use)`,
			platform, currentPlatform,
			platform, currentPlatform,
			getPlatformOS(platform), getPlatformArch(platform), platform,
			platform,
			currentPlatform)
	}

	// Find the current binary
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	fmt.Printf("Development mode: using current platform binary (%s)\n", currentPlatform)
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

// getProjectRoot returns the project root directory
func getProjectRoot() string {
	// Try to find the project root by looking for go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			return "."
		}
		dir = parent
	}
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/so2liu/imgcd/internal/image"
	"github.com/spf13/cobra"
)

var (
	sinceRef       string
	outDir         string
	targetPlatform string
)

var saveCmd = &cobra.Command{
	Use:   "save <IMAGE_REF>",
	Short: "Export a container image to a self-extracting bundle",
	Long: `Export a container image to a self-extracting bundle.

imgcd creates a self-extracting shell script (.sh) that contains the imgcd
binary and image data, allowing import on target systems without installing
imgcd first. Images are automatically pulled for the specified target platform.

The --since flag supports two formats:
  • Full reference: alpine:3.19, myrepo/app:1.0.0
  • Short form (tag only): 3.19, 1.0.0 (uses same repository as main image)

Examples:
  # Export alpine (simplest form, default to linux/amd64)
  imgcd save alpine

  # Full export with tag
  imgcd save ns/app:1.0.0
  # Output: ns_app-1.0.0__since-none.sh

  # Incremental export
  imgcd save alpine:3.20 --since 3.19
  # Output: alpine-3.20__since-3.19.sh

  # Specify target platform
  imgcd save myapp:2.0 --target-platform linux/arm64
  imgcd save myapp:2.0 -t darwin/arm64

  # Export to custom directory
  imgcd save ns/app:2.0.0 --out-dir /tmp/bundles`,
	Args: cobra.ExactArgs(1),
	RunE: runSave,
}

func init() {
	saveCmd.Flags().StringVar(&sinceRef, "since", "", "Base image reference or tag (e.g., 'alpine:3.19' or just '3.19')")
	saveCmd.Flags().StringVar(&outDir, "out-dir", "./out", "Output directory for the exported file")
	saveCmd.Flags().StringVarP(&targetPlatform, "target-platform", "t", "linux/amd64", "Target platform (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)")
}

func runSave(cmd *cobra.Command, args []string) error {
	newRef := args[0]

	// Ensure output directory exists
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Validate target platform
	validPlatforms := []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}
	valid := false
	for _, p := range validPlatforms {
		if p == targetPlatform {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid target platform: %s (valid options: %v)", targetPlatform, validPlatforms)
	}

	// Create exporter
	exporter, err := image.NewExporter(Version)
	if err != nil {
		return fmt.Errorf("failed to create exporter: %w", err)
	}
	defer exporter.Close()

	// Export image
	opts := image.ExportOptions{
		TargetPlatform: targetPlatform,
	}
	outputPath, err := exporter.Export(cmd.Context(), newRef, sinceRef, outDir, opts)
	if err != nil {
		return fmt.Errorf("failed to export image: %w", err)
	}

	absPath, _ := filepath.Abs(outputPath)
	fmt.Printf("✓ Successfully created self-extracting bundle: %s\n", absPath)
	fmt.Printf("\nTo import on target system (%s):\n", targetPlatform)
	fmt.Printf("  chmod +x %s\n", filepath.Base(absPath))
	fmt.Printf("  ./%s\n", filepath.Base(absPath))

	return nil
}

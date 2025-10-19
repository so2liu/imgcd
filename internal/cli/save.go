package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/yangliu35/imgcd/internal/image"
)

var (
	sinceRef string
	outDir   string
)

var saveCmd = &cobra.Command{
	Use:   "save <IMAGE_REF>",
	Short: "Export a container image to a tar.gz file",
	Long: `Export a container image to a tar.gz file with optional incremental export.

The --since flag supports two formats:
  • Full reference: alpine:3.19, myrepo/app:1.0.0
  • Short form (tag only): 3.19, 1.0.0 (uses same repository as main image)

Examples:
  # Full export
  imgcd save ns/app:1.0.0

  # Incremental export with full reference
  imgcd save ns/app:1.2.9 --since ns/app:1.2.8

  # Incremental export with short form (recommended for same repository)
  imgcd save alpine:3.20 --since 3.19
  imgcd save myrepo/app:2.0.0 --since 1.9.0

  # Export to custom directory
  imgcd save ns/app:2.0.0 --since 1.9.0 --out-dir /tmp/bundles`,
	Args: cobra.ExactArgs(1),
	RunE: runSave,
}

func init() {
	saveCmd.Flags().StringVar(&sinceRef, "since", "", "Base image reference or tag (e.g., 'alpine:3.19' or just '3.19')")
	saveCmd.Flags().StringVar(&outDir, "out-dir", "./out", "Output directory for the exported tar.gz file")
}

func runSave(cmd *cobra.Command, args []string) error {
	newRef := args[0]

	// Ensure output directory exists
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create exporter
	exporter, err := image.NewExporter()
	if err != nil {
		return fmt.Errorf("failed to create exporter: %w", err)
	}
	defer exporter.Close()

	// Export image
	outputPath, err := exporter.Export(cmd.Context(), newRef, sinceRef, outDir)
	if err != nil {
		return fmt.Errorf("failed to export image: %w", err)
	}

	absPath, _ := filepath.Abs(outputPath)
	fmt.Printf("✓ Successfully exported image to: %s\n", absPath)

	return nil
}

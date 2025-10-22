package cli

import (
	"fmt"

	"github.com/so2liu/imgcd/internal/image"
	"github.com/spf13/cobra"
)

var fromFile string

var loadCmd = &cobra.Command{
	Use:   "load",
	Short: "Import a container image from a tar.gz file",
	Long: `Import a container image from a tar.gz file created by imgcd save.
The image name and tag are automatically detected from the archive metadata.

Examples:
  # Import image from tar.gz
  imgcd load --from ./out/ns_app-1.2.9__since-1.2.8.tar.gz`,
	RunE: runLoad,
}

func init() {
	loadCmd.Flags().StringVar(&fromFile, "from", "", "Path to the tar.gz file to import (required)")
	loadCmd.MarkFlagRequired("from")
}

func runLoad(cmd *cobra.Command, args []string) error {
	// Create importer
	importer, err := image.NewImporter()
	if err != nil {
		return fmt.Errorf("failed to create importer: %w", err)
	}
	defer importer.Close()

	// Import image
	imageName, err := importer.Import(cmd.Context(), fromFile)
	if err != nil {
		return fmt.Errorf("failed to import image: %w", err)
	}

	fmt.Printf("✓ Successfully imported image: %s\n", imageName)

	return nil
}

package cli

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "imgcd",
	Short: "A tool for incremental container image export/import",
	Long: `imgcd is a CLI tool that allows you to export and import container images
with support for incremental/differential exports. It helps reduce the size
of image transfers in offline environments by only exporting changed layers.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(saveCmd)
	rootCmd.AddCommand(loadCmd)
}

package cli

import (
	"fmt"
	"os"

	"github.com/blang/semver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update imgcd to the latest version",
	Long:  `Check for the latest version of imgcd and automatically update if a newer version is available.`,
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	fmt.Printf("Current version: %s\n", Version)
	fmt.Println("Checking for updates...")

	// Check for latest release
	latest, found, err := selfupdate.DetectLatest("so2liu/imgcd")
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	if !found {
		return fmt.Errorf("no release found")
	}

	// Parse current version (skip comparison for dev builds)
	if Version != "dev" {
		currentVersion, err := semver.Parse(stripVersionPrefix(Version))
		if err != nil {
			return fmt.Errorf("failed to parse current version: %w", err)
		}

		// Compare versions
		if latest.Version.LTE(currentVersion) {
			fmt.Printf("Already up to date (version %s)\n", Version)
			return nil
		}
	}

	fmt.Printf("New version available: %s\n", latest.Version)
	fmt.Println("Updating...")

	// Get current executable path
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Update the binary
	if err := selfupdate.UpdateTo(latest.AssetURL, exe); err != nil {
		return fmt.Errorf("failed to update: %w", err)
	}

	fmt.Printf("Successfully updated to version %s\n", latest.Version)
	fmt.Println("Please restart imgcd to use the new version")

	return nil
}

// stripVersionPrefix removes 'v' prefix from version string if present
func stripVersionPrefix(version string) string {
	if len(version) > 0 && version[0] == 'v' {
		return version[1:]
	}
	return version
}

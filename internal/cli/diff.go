package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/so2liu/imgcd/internal/diff"
	"github.com/so2liu/imgcd/internal/prompt"
	"github.com/so2liu/imgcd/internal/remote"
	"github.com/spf13/cobra"
)

var (
	diffSinceRef       string
	diffTargetPlatform string
	diffVerbose        bool
	diffOutput         string
)

var diffCmd = &cobra.Command{
	Use:   "diff <IMAGE_REF>",
	Short: "Compare images and show layer differences without downloading",
	Long: `Compare two container images and show layer differences.

This command fetches only image metadata (manifests and configs) from the
registry without downloading actual layer data. It's useful for quickly
estimating the size of incremental exports.

The --since flag is required and supports two formats:
  • Full reference: alpine:3.19, myrepo/app:1.0.0
  • Short form (tag only): 3.19, 1.0.0 (uses same repository as main image)

Examples:
  # Compare two alpine versions
  imgcd diff alpine:3.20 --since 3.19

  # Detailed output with layer information
  imgcd diff alpine:3.20 --since 3.19 --verbose

  # JSON output for scripting
  imgcd diff alpine:3.20 --since 3.19 --output json

  # Specify target platform
  imgcd diff myapp:2.0 --since 1.9 --target-platform linux/arm64
  imgcd diff myapp:2.0 --since 1.9 -t darwin/arm64`,
	Args: cobra.ExactArgs(1),
	RunE: runDiff,
}

func init() {
	diffCmd.Flags().StringVar(&diffSinceRef, "since", "", "Base image reference or tag (required)")
	diffCmd.MarkFlagRequired("since")
	diffCmd.Flags().StringVarP(&diffTargetPlatform, "target-platform", "t", "linux/amd64", "Target platform (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)")
	diffCmd.Flags().BoolVarP(&diffVerbose, "verbose", "v", false, "Show detailed layer information")
	diffCmd.Flags().StringVar(&diffOutput, "output", "text", "Output format: text or json")
}

func runDiff(cmd *cobra.Command, args []string) error {
	newRef := args[0]

	// Validate --since is provided
	if diffSinceRef == "" {
		return fmt.Errorf("--since flag is required")
	}

	// Resolve base reference with fuzzy matching
	var baseRef string
	if !strings.Contains(diffSinceRef, "/") && !strings.Contains(diffSinceRef, ":") {
		// Short tag format - resolve with exact-first-then-fuzzy logic
		repo := newRef
		if idx := lastIndex(repo, ":"); idx != -1 {
			repo = repo[:idx]
		}

		fetcher := remote.NewFetcher()
		exactTag, matches, err := fetcher.ResolveTag(cmd.Context(), repo, diffSinceRef)
		if err != nil {
			return err
		}

		if exactTag != "" {
			// Exact or single fuzzy match
			if exactTag != diffSinceRef {
				fmt.Printf("Resolved --since %q to tag: %s\n", diffSinceRef, exactTag)
			}
			baseRef = fmt.Sprintf("%s:%s", repo, exactTag)
		} else {
			// Multiple matches - prompt user
			selected, err := prompt.PromptSelection(
				fmt.Sprintf("Multiple tags found matching %q:", diffSinceRef),
				matches,
			)
			if err != nil {
				return err
			}
			fmt.Printf("Selected: %s\n", selected)
			baseRef = fmt.Sprintf("%s:%s", repo, selected)
		}
	} else {
		baseRef = normalizeReference(newRef, diffSinceRef)
	}

	// Validate target platform
	validPlatforms := []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}
	valid := false
	for _, p := range validPlatforms {
		if p == diffTargetPlatform {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid target platform: %s (valid options: %v)", diffTargetPlatform, validPlatforms)
	}

	// Validate output format
	var outputFormat diff.OutputFormat
	switch diffOutput {
	case "text":
		outputFormat = diff.OutputFormatText
	case "json":
		outputFormat = diff.OutputFormatJSON
	default:
		return fmt.Errorf("invalid output format: %s (valid options: text, json)", diffOutput)
	}

	// Create fetcher and differ
	fetcher := remote.NewFetcher()
	differ := diff.NewDiffer(fetcher)

	// Perform comparison
	result, err := differ.Compare(cmd.Context(), newRef, baseRef, diffTargetPlatform)
	if err != nil {
		return fmt.Errorf("failed to compare images: %w", err)
	}

	// Format and output result
	formatter := diff.NewFormatter(diff.FormatOptions{
		Format:  outputFormat,
		Verbose: diffVerbose,
	})

	if err := formatter.Format(os.Stdout, result); err != nil {
		return fmt.Errorf("failed to format output: %w", err)
	}

	return nil
}

// normalizeReference converts a short tag to a full reference
// e.g., normalizeReference("alpine:3.20", "3.19") -> "alpine:3.19"
func normalizeReference(mainRef, sinceRef string) string {
	// If sinceRef already contains ":" or "/", it's a full reference
	if containsAny(sinceRef, []string{":", "/"}) {
		return sinceRef
	}

	// Extract repository from mainRef and append sinceRef as tag
	// Remove tag from mainRef if present
	repo := mainRef
	if idx := lastIndex(repo, ":"); idx != -1 {
		repo = repo[:idx]
	}

	return repo + ":" + sinceRef
}

// containsAny checks if s contains any of the substrings
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
	}
	return false
}

// lastIndex returns the last index of substr in s, or -1 if not found
func lastIndex(s, substr string) int {
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

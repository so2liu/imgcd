package diff

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OutputFormat represents the output format type
type OutputFormat string

const (
	OutputFormatText OutputFormat = "text"
	OutputFormatJSON OutputFormat = "json"
)

// FormatOptions contains options for formatting output
type FormatOptions struct {
	Format  OutputFormat
	Verbose bool
}

// Formatter formats diff results for output
type Formatter struct {
	options FormatOptions
}

// NewFormatter creates a new Formatter with the given options
func NewFormatter(options FormatOptions) *Formatter {
	return &Formatter{
		options: options,
	}
}

// Format writes the formatted diff result to the writer
func (f *Formatter) Format(w io.Writer, result *DiffResult) error {
	switch f.options.Format {
	case OutputFormatJSON:
		return f.formatJSON(w, result)
	case OutputFormatText:
		return f.formatText(w, result)
	default:
		return fmt.Errorf("unsupported output format: %s", f.options.Format)
	}
}

// formatJSON outputs the result as JSON
func (f *Formatter) formatJSON(w io.Writer, result *DiffResult) error {
	output := map[string]interface{}{
		"newImage":   result.NewImage.Reference,
		"baseImage":  result.BaseImage.Reference,
		"platform":   result.NewImage.Platform,
		"newDigest":  result.NewImage.Digest.String(),
		"baseDigest": result.BaseImage.Digest.String(),
		"summary": map[string]interface{}{
			"totalLayers":       len(result.LayerDiffs),
			"newLayers":         len(result.NewLayers),
			"sharedLayers":      len(result.SharedLayers),
			"newLayersSize":     result.NewLayersSize,
			"sharedLayersSize":  result.SharedLayersSize,
			"totalSize":         result.TotalNewImageSize,
			"savingsSize":       result.SavingsSize,
			"savingsPercentage": result.SavingsPercentage,
		},
	}

	if f.options.Verbose {
		layers := make([]map[string]interface{}, 0, len(result.LayerDiffs))
		for _, layer := range result.LayerDiffs {
			layers = append(layers, map[string]interface{}{
				"diffId":  layer.DiffID.String(),
				"digest":  layer.Digest.String(),
				"size":    layer.Size,
				"command": layer.Command,
				"status":  string(layer.Status),
			})
		}
		output["layers"] = layers
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// formatText outputs the result as human-readable text
func (f *Formatter) formatText(w io.Writer, result *DiffResult) error {
	// Header
	fmt.Fprintf(w, "Image:    %s\n", result.NewImage.Reference)
	fmt.Fprintf(w, "Base:     %s\n", result.BaseImage.Reference)
	fmt.Fprintf(w, "Platform: %s\n", result.NewImage.Platform)
	fmt.Fprintln(w)

	// Verbose mode: show layer details
	if f.options.Verbose {
		fmt.Fprintln(w, "Layer Details:")
		for _, layer := range result.LayerDiffs {
			status := "SHARED"
			if layer.Status == LayerStatusNew {
				status = "NEW   "
			}

			// Truncate DiffID for readability
			diffIDShort := layer.DiffID.String()
			if len(diffIDShort) > 19 {
				diffIDShort = diffIDShort[:19] + "..."
			}

			// Truncate command for readability
			command := layer.Command
			if len(command) > 60 {
				command = command[:57] + "..."
			}
			// Clean up command - remove leading "/bin/sh -c " or similar
			command = strings.TrimPrefix(command, "/bin/sh -c ")
			command = strings.TrimPrefix(command, "RUN ")

			fmt.Fprintf(w, "  [%s] %s (%s)",
				status,
				diffIDShort,
				formatSize(layer.Size),
			)
			if command != "" {
				fmt.Fprintf(w, " - %s", command)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}

	// Summary
	fmt.Fprintln(w, "Summary:")
	fmt.Fprintf(w, "  Total layers:   %d\n", len(result.LayerDiffs))
	fmt.Fprintf(w, "  New layers:     %d\n", len(result.NewLayers))
	fmt.Fprintf(w, "  Shared layers:  %d\n", len(result.SharedLayers))
	fmt.Fprintln(w)

	// Size information
	fmt.Fprintln(w, "Size Analysis:")
	fmt.Fprintf(w, "  Incremental export: %s (new layers only)\n", formatSize(result.NewLayersSize))
	fmt.Fprintf(w, "  Full export:        %s (all layers)\n", formatSize(result.TotalNewImageSize))
	fmt.Fprintf(w, "  Space savings:      %s (%.1f%%)\n",
		formatSize(result.SavingsSize),
		result.SavingsPercentage,
	)

	return nil
}

// formatSize formats a byte size into a human-readable string
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

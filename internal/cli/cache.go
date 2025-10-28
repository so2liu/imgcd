package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/so2liu/imgcd/internal/cache"
	"github.com/spf13/cobra"
)

var (
	cacheForce    bool
	cachePruneAge int
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage imgcd layer cache",
	Long: `Manage the local layer cache used by imgcd when downloading images in remote mode.

The cache is stored at ~/.imgcd/cache/ and helps avoid re-downloading the same layers.

Available commands:
  list   - List all cached layers
  clean  - Remove all cached layers
  prune  - Remove old/unused cached layers
  info   - Show cache statistics`,
}

var cacheListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all cached layers",
	Long: `List all layers currently in the cache.

Shows layer ID (short format), size, source image, and last access time.`,
	RunE: runCacheList,
}

var cacheCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove all cached layers",
	Long: `Remove all layers from the cache.

This will free up all disk space used by the cache. You will need to re-download
layers on the next export.

Use --force to skip confirmation prompt.`,
	RunE: runCacheClean,
}

var cachePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove old/unused cached layers",
	Long: `Remove layers that haven't been accessed in a specified number of days.

By default, removes layers not accessed in the last 30 days.

Use --days to specify a different age threshold.`,
	RunE: runCachePrune,
}

var cacheInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show cache statistics",
	Long: `Display statistics about the layer cache.

Shows total cache size, number of layers, cache hit/miss rates (since last start),
and last prune time.`,
	RunE: runCacheInfo,
}

func init() {
	// Add cache subcommands
	cacheCmd.AddCommand(cacheListCmd)
	cacheCmd.AddCommand(cacheCleanCmd)
	cacheCmd.AddCommand(cachePruneCmd)
	cacheCmd.AddCommand(cacheInfoCmd)

	// Add flags
	cacheCleanCmd.Flags().BoolVarP(&cacheForce, "force", "f", false, "Skip confirmation prompt")
	cachePruneCmd.Flags().IntVar(&cachePruneAge, "days", 30, "Remove layers not accessed in this many days")
}

func runCacheList(cmd *cobra.Command, args []string) error {
	lc, err := cache.NewLayerCache(true)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	layers := lc.List()
	if len(layers) == 0 {
		fmt.Println("Cache is empty")
		return nil
	}

	// Sort by last access time (newest first)
	sort.Slice(layers, func(i, j int) bool {
		return layers[i].LastAccess.After(layers[j].LastAccess)
	})

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LAYER ID\tSIZE\tIMAGE\tLAST ACCESSED")

	for _, layer := range layers {
		shortID := getShortID(layer.DiffID)
		size := formatSize(layer.Size)
		imageRef := formatImageRef(layer.ImageRef)
		lastAccess := formatTime(layer.LastAccess)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shortID, size, imageRef, lastAccess)
	}

	w.Flush()

	// Print summary
	stats := lc.GetStats()
	fmt.Printf("\nTotal: %d layers, %s\n", stats.LayerCount, formatSize(stats.TotalSize))

	return nil
}

func runCacheClean(cmd *cobra.Command, args []string) error {
	lc, err := cache.NewLayerCache(true)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	stats := lc.GetStats()
	if stats.LayerCount == 0 {
		fmt.Println("Cache is already empty")
		return nil
	}

	// Ask for confirmation unless --force is used
	if !cacheForce {
		fmt.Printf("This will remove all %d cached layers (%s).\n", stats.LayerCount, formatSize(stats.TotalSize))
		fmt.Print("Are you sure? (y/N): ")

		var response string
		fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))

		if response != "y" && response != "yes" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	// Clean cache
	if err := lc.Clean(); err != nil {
		return fmt.Errorf("failed to clean cache: %w", err)
	}

	fmt.Printf("✓ Successfully cleaned cache (freed %s)\n", formatSize(stats.TotalSize))

	return nil
}

func runCachePrune(cmd *cobra.Command, args []string) error {
	lc, err := cache.NewLayerCache(true)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	maxAge := time.Duration(cachePruneAge) * 24 * time.Hour

	fmt.Printf("Pruning layers not accessed in the last %d days...\n", cachePruneAge)

	count, freedSpace, err := lc.Prune(maxAge)
	if err != nil {
		return fmt.Errorf("failed to prune cache: %w", err)
	}

	if count == 0 {
		fmt.Println("No layers to prune")
		return nil
	}

	fmt.Printf("✓ Successfully pruned %d layers (freed %s)\n", count, formatSize(freedSpace))

	return nil
}

func runCacheInfo(cmd *cobra.Command, args []string) error {
	lc, err := cache.NewLayerCache(true)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	stats := lc.GetStats()

	fmt.Println("Cache Statistics:")
	fmt.Printf("  Location:     ~/.imgcd/cache/\n")
	fmt.Printf("  Total size:   %s\n", formatSize(stats.TotalSize))
	fmt.Printf("  Layer count:  %d\n", stats.LayerCount)

	// Show cache hit/miss only if there's activity
	if stats.CacheHits > 0 || stats.CacheMisses > 0 {
		total := stats.CacheHits + stats.CacheMisses
		hitRate := float64(stats.CacheHits) / float64(total) * 100
		fmt.Printf("\nCache Activity (this session):\n")
		fmt.Printf("  Cache hits:   %d\n", stats.CacheHits)
		fmt.Printf("  Cache misses: %d\n", stats.CacheMisses)
		fmt.Printf("  Hit rate:     %.1f%%\n", hitRate)
	}

	if !stats.LastPruneAt.IsZero() {
		fmt.Printf("\nLast prune:   %s\n", formatTime(stats.LastPruneAt))
	}

	return nil
}

// Helper functions

func getShortID(diffID string) string {
	hash := strings.TrimPrefix(diffID, "sha256:")
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes < KB:
		return fmt.Sprintf("%dB", bytes)
	case bytes < MB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/KB)
	case bytes < GB:
		return fmt.Sprintf("%.1fMB", float64(bytes)/MB)
	default:
		return fmt.Sprintf("%.2fGB", float64(bytes)/GB)
	}
}

func formatImageRef(ref string) string {
	// Truncate long image references
	if len(ref) > 40 {
		return ref[:37] + "..."
	}
	return ref
}

func formatTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}

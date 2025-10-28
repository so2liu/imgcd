package cache

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LayerMetadata contains metadata about a cached layer
type LayerMetadata struct {
	DiffID     string    `json:"diffid"`      // Uncompressed digest (cache key)
	Digest     string    `json:"digest"`      // Compressed digest
	Size       int64     `json:"size"`        // Uncompressed size
	ImageRef   string    `json:"image_ref"`   // Source image reference (e.g., "alpine:3.20")
	LastAccess time.Time `json:"last_access"` // Last time this layer was accessed
	CreatedAt  time.Time `json:"created_at"`  // When this layer was first cached
}

// CacheStats contains statistics about the cache
type CacheStats struct {
	TotalSize   int64 // Total size of all cached layers
	LayerCount  int   // Number of cached layers
	CacheHits   int64 // Number of cache hits (not persisted)
	CacheMisses int64 // Number of cache misses (not persisted)
	LastPruneAt time.Time
}

// LayerCache manages the local layer cache
type LayerCache struct {
	cacheDir     string
	metadataPath string
	metadata     map[string]*LayerMetadata
	stats        *CacheStats
	mu           sync.RWMutex
	enabled      bool
}

// NewLayerCache creates a new layer cache
func NewLayerCache(enabled bool) (*LayerCache, error) {
	if !enabled {
		return &LayerCache{enabled: false}, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".imgcd", "cache", "layers", "sha256")
	metadataPath := filepath.Join(homeDir, ".imgcd", "cache", "metadata.json")

	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	lc := &LayerCache{
		cacheDir:     cacheDir,
		metadataPath: metadataPath,
		metadata:     make(map[string]*LayerMetadata),
		stats:        &CacheStats{},
		enabled:      true,
	}

	// Load existing metadata
	if err := lc.loadMetadata(); err != nil {
		// If metadata doesn't exist or is corrupt, start fresh
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to load cache metadata: %v\n", err)
		}
	}

	return lc, nil
}

// Exists checks if a layer exists in the cache
func (lc *LayerCache) Exists(diffID string) bool {
	if !lc.enabled {
		return false
	}

	lc.mu.RLock()
	defer lc.mu.RUnlock()

	shortID := lc.getShortID(diffID)
	_, exists := lc.metadata[shortID]
	return exists
}

// Get retrieves a layer from the cache
func (lc *LayerCache) Get(diffID string) (io.ReadCloser, error) {
	if !lc.enabled {
		return nil, fmt.Errorf("cache is disabled")
	}

	lc.mu.Lock()
	defer lc.mu.Unlock()

	shortID := lc.getShortID(diffID)
	meta, exists := lc.metadata[shortID]
	if !exists {
		lc.stats.CacheMisses++
		return nil, fmt.Errorf("layer not in cache")
	}

	layerPath := lc.getLayerPath(shortID)
	file, err := os.Open(layerPath)
	if err != nil {
		// Cache entry exists but file is missing, remove from metadata
		delete(lc.metadata, shortID)
		lc.saveMetadata()
		lc.stats.CacheMisses++
		return nil, fmt.Errorf("cached layer file not found: %w", err)
	}

	// Update last access time
	meta.LastAccess = time.Now()
	lc.saveMetadata()
	lc.stats.CacheHits++

	return file, nil
}

// Put saves a layer to the cache
func (lc *LayerCache) Put(diffID string, reader io.Reader, imageRef string, size int64) error {
	if !lc.enabled {
		return nil
	}

	lc.mu.Lock()
	defer lc.mu.Unlock()

	shortID := lc.getShortID(diffID)
	layerPath := lc.getLayerPath(shortID)

	// Create layer directory
	layerDir := filepath.Dir(layerPath)
	if err := os.MkdirAll(layerDir, 0755); err != nil {
		return fmt.Errorf("failed to create layer directory: %w", err)
	}

	// Write layer to cache
	file, err := os.Create(layerPath)
	if err != nil {
		return fmt.Errorf("failed to create cache file: %w", err)
	}
	defer file.Close()

	written, err := io.Copy(file, reader)
	if err != nil {
		os.Remove(layerPath)
		return fmt.Errorf("failed to write layer to cache: %w", err)
	}

	// Add metadata
	now := time.Now()
	lc.metadata[shortID] = &LayerMetadata{
		DiffID:     diffID,
		Size:       size,
		ImageRef:   lc.normalizeImageRef(imageRef),
		LastAccess: now,
		CreatedAt:  now,
	}

	// Update stats
	lc.stats.TotalSize += written
	lc.stats.LayerCount = len(lc.metadata)

	// Save metadata
	return lc.saveMetadata()
}

// List returns all cached layers
func (lc *LayerCache) List() []*LayerMetadata {
	if !lc.enabled {
		return nil
	}

	lc.mu.RLock()
	defer lc.mu.RUnlock()

	layers := make([]*LayerMetadata, 0, len(lc.metadata))
	for _, meta := range lc.metadata {
		layers = append(layers, meta)
	}

	return layers
}

// Clean removes all cached layers
func (lc *LayerCache) Clean() error {
	if !lc.enabled {
		return nil
	}

	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Remove all layer files
	cacheRoot := filepath.Dir(lc.cacheDir)
	if err := os.RemoveAll(cacheRoot); err != nil {
		return fmt.Errorf("failed to remove cache directory: %w", err)
	}

	// Recreate directory structure
	if err := os.MkdirAll(lc.cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to recreate cache directory: %w", err)
	}

	// Reset metadata
	lc.metadata = make(map[string]*LayerMetadata)
	lc.stats = &CacheStats{}

	return lc.saveMetadata()
}

// Prune removes layers that haven't been accessed in maxAge
func (lc *LayerCache) Prune(maxAge time.Duration) (int, int64, error) {
	if !lc.enabled {
		return 0, 0, nil
	}

	lc.mu.Lock()
	defer lc.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	toRemove := []string{}
	var freedSpace int64

	// Find layers to remove
	for shortID, meta := range lc.metadata {
		if meta.LastAccess.Before(cutoff) {
			toRemove = append(toRemove, shortID)

			// Get actual file size
			layerPath := lc.getLayerPath(shortID)
			if info, err := os.Stat(layerPath); err == nil {
				freedSpace += info.Size()
			}
		}
	}

	// Remove layers
	for _, shortID := range toRemove {
		layerPath := lc.getLayerPath(shortID)
		os.Remove(layerPath)

		// Remove directory if empty
		layerDir := filepath.Dir(layerPath)
		os.Remove(layerDir)

		delete(lc.metadata, shortID)
	}

	// Update stats
	lc.stats.TotalSize -= freedSpace
	lc.stats.LayerCount = len(lc.metadata)
	lc.stats.LastPruneAt = time.Now()

	if err := lc.saveMetadata(); err != nil {
		return len(toRemove), freedSpace, err
	}

	return len(toRemove), freedSpace, nil
}

// GetStats returns cache statistics
func (lc *LayerCache) GetStats() *CacheStats {
	if !lc.enabled {
		return &CacheStats{}
	}

	lc.mu.RLock()
	defer lc.mu.RUnlock()

	// Recalculate actual disk usage
	var totalSize int64
	for shortID := range lc.metadata {
		layerPath := lc.getLayerPath(shortID)
		if info, err := os.Stat(layerPath); err == nil {
			totalSize += info.Size()
		}
	}

	return &CacheStats{
		TotalSize:   totalSize,
		LayerCount:  len(lc.metadata),
		CacheHits:   lc.stats.CacheHits,
		CacheMisses: lc.stats.CacheMisses,
		LastPruneAt: lc.stats.LastPruneAt,
	}
}

// loadMetadata loads metadata from disk
func (lc *LayerCache) loadMetadata() error {
	data, err := os.ReadFile(lc.metadataPath)
	if err != nil {
		return err
	}

	var metadata map[string]*LayerMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}

	lc.metadata = metadata

	// Recalculate stats
	var totalSize int64
	for shortID := range lc.metadata {
		layerPath := lc.getLayerPath(shortID)
		if info, err := os.Stat(layerPath); err == nil {
			totalSize += info.Size()
		}
	}
	lc.stats.TotalSize = totalSize
	lc.stats.LayerCount = len(lc.metadata)

	return nil
}

// saveMetadata saves metadata to disk
func (lc *LayerCache) saveMetadata() error {
	data, err := json.MarshalIndent(lc.metadata, "", "  ")
	if err != nil {
		return err
	}

	metadataDir := filepath.Dir(lc.metadataPath)
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		return err
	}

	return os.WriteFile(lc.metadataPath, data, 0644)
}

// getShortID extracts the short ID (first 12 chars of hash) from a digest
func (lc *LayerCache) getShortID(diffID string) string {
	// Remove "sha256:" prefix if present
	hash := strings.TrimPrefix(diffID, "sha256:")
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// getLayerPath returns the path to a cached layer file
func (lc *LayerCache) getLayerPath(shortID string) string {
	return filepath.Join(lc.cacheDir, shortID, "layer.tar.gz")
}

// normalizeImageRef removes tag if it's "latest" or contains digest
func (lc *LayerCache) normalizeImageRef(ref string) string {
	// Remove "@sha256:..." if present
	if idx := strings.Index(ref, "@"); idx != -1 {
		ref = ref[:idx]
	}

	// Keep the tag for better tracking
	return ref
}

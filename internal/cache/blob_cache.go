package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BlobMetadata contains metadata about a cached blob
type BlobMetadata struct {
	Digest     string    `json:"digest"`      // Compressed digest (cache key, with sha256: prefix)
	DiffID     string    `json:"diffid"`      // Uncompressed digest
	Size       int64     `json:"size"`        // Compressed size
	ImageRefs  []string  `json:"image_refs"`  // Source image references (multiple images may share this blob)
	LastAccess time.Time `json:"last_access"` // Last time this blob was accessed
	CreatedAt  time.Time `json:"created_at"`  // When this blob was first cached
}

// BlobCacheIndex contains the index of all cached blobs
type BlobCacheIndex struct {
	Version   string                   `json:"version"` // Index format version
	Blobs     map[string]*BlobMetadata `json:"blobs"`   // digest -> metadata
	CreatedAt time.Time                `json:"created_at"`
	UpdatedAt time.Time                `json:"updated_at"`
}

// BlobCache manages the local blob cache
// Unlike the old LayerCache, this stores registry blobs directly (compressed)
// without any decompression/recompression, using digest as the key
type BlobCache struct {
	cacheDir  string
	indexPath string
	index     *BlobCacheIndex
	mu        sync.RWMutex
	enabled   bool
}

// NewBlobCache creates a new blob cache
func NewBlobCache(enabled bool) (*BlobCache, error) {
	if !enabled {
		return &BlobCache{enabled: false}, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".imgcd", "cache", "blobs", "sha256")
	indexPath := filepath.Join(homeDir, ".imgcd", "cache", "index.json")

	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	bc := &BlobCache{
		cacheDir:  cacheDir,
		indexPath: indexPath,
		index: &BlobCacheIndex{
			Version:   "2",
			Blobs:     make(map[string]*BlobMetadata),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		enabled: true,
	}

	// Load existing index
	if err := bc.loadIndex(); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to load cache index: %v\n", err)
		}
	}

	return bc, nil
}

// Exists checks if a blob exists in the cache by digest
func (bc *BlobCache) Exists(digest string) bool {
	if !bc.enabled {
		return false
	}

	bc.mu.RLock()
	defer bc.mu.RUnlock()

	digest = bc.normalizeDigest(digest)
	_, exists := bc.index.Blobs[digest]
	return exists
}

// Get retrieves a blob from the cache
// Returns an io.ReadCloser for the compressed blob
func (bc *BlobCache) Get(digest string) (io.ReadCloser, error) {
	if !bc.enabled {
		return nil, fmt.Errorf("cache is disabled")
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	digest = bc.normalizeDigest(digest)
	meta, exists := bc.index.Blobs[digest]
	if !exists {
		return nil, fmt.Errorf("blob not in cache")
	}

	blobPath := bc.getBlobPath(digest)
	file, err := os.Open(blobPath)
	if err != nil {
		// Cache entry exists but file is missing, remove from index
		delete(bc.index.Blobs, digest)
		bc.saveIndex()
		return nil, fmt.Errorf("cached blob file not found: %w", err)
	}

	// Update last access time
	meta.LastAccess = time.Now()
	bc.index.UpdatedAt = time.Now()
	bc.saveIndex()

	return file, nil
}

// Put saves a blob to the cache
// reader should be the compressed blob data from the registry
// digest verification is performed during write
func (bc *BlobCache) Put(digest, diffID string, reader io.Reader, imageRef string) error {
	if !bc.enabled {
		return nil
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	digest = bc.normalizeDigest(digest)
	diffID = bc.normalizeDigest(diffID)

	// Check if already exists
	if meta, exists := bc.index.Blobs[digest]; exists {
		// Update image refs if not already present
		if !bc.containsImageRef(meta.ImageRefs, imageRef) {
			meta.ImageRefs = append(meta.ImageRefs, imageRef)
			meta.LastAccess = time.Now()
			bc.index.UpdatedAt = time.Now()
			return bc.saveIndex()
		}
		return nil
	}

	blobPath := bc.getBlobPath(digest)

	// Create blob directory
	blobDir := filepath.Dir(blobPath)
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return fmt.Errorf("failed to create blob directory: %w", err)
	}

	// Write blob to cache with digest verification
	file, err := os.Create(blobPath)
	if err != nil {
		return fmt.Errorf("failed to create cache file: %w", err)
	}
	defer file.Close()

	// Calculate digest while writing
	hasher := sha256.New()
	tee := io.TeeReader(reader, hasher)

	written, err := io.Copy(file, tee)
	if err != nil {
		os.Remove(blobPath)
		return fmt.Errorf("failed to write blob to cache: %w", err)
	}

	// Verify digest matches
	calculatedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if calculatedDigest != digest {
		os.Remove(blobPath)
		return fmt.Errorf("digest mismatch: expected %s, got %s", digest, calculatedDigest)
	}

	// Add metadata
	now := time.Now()
	bc.index.Blobs[digest] = &BlobMetadata{
		Digest:     digest,
		DiffID:     diffID,
		Size:       written,
		ImageRefs:  []string{imageRef},
		LastAccess: now,
		CreatedAt:  now,
	}
	bc.index.UpdatedAt = now

	// Save index
	return bc.saveIndex()
}

// GetMetadata returns metadata for a blob
func (bc *BlobCache) GetMetadata(digest string) (*BlobMetadata, error) {
	if !bc.enabled {
		return nil, fmt.Errorf("cache is disabled")
	}

	bc.mu.RLock()
	defer bc.mu.RUnlock()

	digest = bc.normalizeDigest(digest)
	meta, exists := bc.index.Blobs[digest]
	if !exists {
		return nil, fmt.Errorf("blob not in cache")
	}

	return meta, nil
}

// List returns all cached blobs
func (bc *BlobCache) List() []*BlobMetadata {
	if !bc.enabled {
		return nil
	}

	bc.mu.RLock()
	defer bc.mu.RUnlock()

	blobs := make([]*BlobMetadata, 0, len(bc.index.Blobs))
	for _, meta := range bc.index.Blobs {
		blobs = append(blobs, meta)
	}

	return blobs
}

// Clean removes all cached blobs
func (bc *BlobCache) Clean() error {
	if !bc.enabled {
		return nil
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	// Remove all blob files
	cacheRoot := filepath.Join(filepath.Dir(bc.cacheDir), "..")
	if err := os.RemoveAll(cacheRoot); err != nil {
		return fmt.Errorf("failed to remove cache directory: %w", err)
	}

	// Recreate directory structure
	if err := os.MkdirAll(bc.cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to recreate cache directory: %w", err)
	}

	// Reset index
	now := time.Now()
	bc.index = &BlobCacheIndex{
		Version:   "2",
		Blobs:     make(map[string]*BlobMetadata),
		CreatedAt: now,
		UpdatedAt: now,
	}

	return bc.saveIndex()
}

// Prune removes blobs that haven't been accessed in maxAge
func (bc *BlobCache) Prune(maxAge time.Duration) (int, int64, error) {
	if !bc.enabled {
		return 0, 0, nil
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	toRemove := []string{}
	var freedSpace int64

	// Find blobs to remove
	for digest, meta := range bc.index.Blobs {
		if meta.LastAccess.Before(cutoff) {
			toRemove = append(toRemove, digest)
			freedSpace += meta.Size
		}
	}

	// Remove blobs
	for _, digest := range toRemove {
		blobPath := bc.getBlobPath(digest)
		os.Remove(blobPath)

		// Remove directory if empty
		blobDir := filepath.Dir(blobPath)
		os.Remove(blobDir)

		delete(bc.index.Blobs, digest)
	}

	bc.index.UpdatedAt = time.Now()

	if err := bc.saveIndex(); err != nil {
		return len(toRemove), freedSpace, err
	}

	return len(toRemove), freedSpace, nil
}

// GetStats returns cache statistics
func (bc *BlobCache) GetStats() (totalSize int64, blobCount int) {
	if !bc.enabled {
		return 0, 0
	}

	bc.mu.RLock()
	defer bc.mu.RUnlock()

	// Calculate total size
	for _, meta := range bc.index.Blobs {
		totalSize += meta.Size
	}

	return totalSize, len(bc.index.Blobs)
}

// loadIndex loads index from disk
func (bc *BlobCache) loadIndex() error {
	data, err := os.ReadFile(bc.indexPath)
	if err != nil {
		return err
	}

	var index BlobCacheIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}

	// Validate version
	if index.Version != "2" {
		return fmt.Errorf("unsupported cache version: %s (expected 2)", index.Version)
	}

	bc.index = &index
	return nil
}

// saveIndex saves index to disk
func (bc *BlobCache) saveIndex() error {
	data, err := json.MarshalIndent(bc.index, "", "  ")
	if err != nil {
		return err
	}

	indexDir := filepath.Dir(bc.indexPath)
	if err := os.MkdirAll(indexDir, 0755); err != nil {
		return err
	}

	return os.WriteFile(bc.indexPath, data, 0644)
}

// getBlobPath returns the path to a cached blob file
func (bc *BlobCache) getBlobPath(digest string) string {
	// Remove sha256: prefix
	hash := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(bc.cacheDir, hash)
}

// normalizeDigest ensures digest has sha256: prefix
func (bc *BlobCache) normalizeDigest(digest string) string {
	if !strings.HasPrefix(digest, "sha256:") {
		return "sha256:" + digest
	}
	return digest
}

// containsImageRef checks if an image ref is in the list
func (bc *BlobCache) containsImageRef(refs []string, ref string) bool {
	for _, r := range refs {
		if r == ref {
			return true
		}
	}
	return false
}

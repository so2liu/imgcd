package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/so2liu/imgcd/internal/cache"
)

// BlobDownloader handles downloading compressed blobs from registry
type BlobDownloader struct {
	blobCache *cache.BlobCache
	debug     bool
}

// NewBlobDownloader creates a new blob downloader
func NewBlobDownloader(blobCache *cache.BlobCache) *BlobDownloader {
	return &BlobDownloader{
		blobCache: blobCache,
		debug:     os.Getenv("IMGCD_DEBUG") != "",
	}
}

// DownloadResult represents the result of a blob download
type DownloadResult struct {
	Digest    string
	DiffID    string
	Size      int64
	FromCache bool
	Err       error
}

// DownloadBlobs downloads multiple blobs in parallel
// layers: the layers to download (from go-containerregistry)
// imageRef: the source image reference (for cache tracking)
// maxConcurrency: maximum number of concurrent downloads (0 = unlimited)
func (bd *BlobDownloader) DownloadBlobs(ctx context.Context, layers []v1.Layer, imageRef string, maxConcurrency int) ([]DownloadResult, error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 4 // Default to 4 concurrent downloads
	}

	results := make([]DownloadResult, len(layers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)

	for i, layer := range layers {
		wg.Add(1)
		go func(index int, l v1.Layer) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check if cancelled
			select {
			case <-ctx.Done():
				results[index] = DownloadResult{Err: ctx.Err()}
				return
			default:
			}

			// Download blob
			result := bd.downloadSingleBlob(ctx, l, imageRef)
			results[index] = result
		}(i, layer)
	}

	wg.Wait()

	// Check for errors
	for i, result := range results {
		if result.Err != nil {
			return results, fmt.Errorf("failed to download layer %d: %w", i, result.Err)
		}
	}

	return results, nil
}

// downloadSingleBlob downloads a single blob
func (bd *BlobDownloader) downloadSingleBlob(ctx context.Context, layer v1.Layer, imageRef string) DownloadResult {
	// Get digest (compressed)
	digest, err := layer.Digest()
	if err != nil {
		return DownloadResult{Err: fmt.Errorf("failed to get digest: %w", err)}
	}

	// Get diffID (uncompressed)
	diffID, err := layer.DiffID()
	if err != nil {
		return DownloadResult{Err: fmt.Errorf("failed to get diffID: %w", err)}
	}

	digestStr := digest.String()
	diffIDStr := diffID.String()

	// Check cache first
	if bd.blobCache.Exists(digestStr) {
		if bd.debug {
			fmt.Fprintf(os.Stderr, "[DEBUG] Blob %s already cached\n", digestStr[:19])
		}

		// Still update metadata to track this image reference
		cachedReader, err := bd.blobCache.Get(digestStr)
		if err == nil {
			cachedReader.Close() // We just needed to update access time
			return DownloadResult{
				Digest:    digestStr,
				DiffID:    diffIDStr,
				FromCache: true,
			}
		}
	}

	if bd.debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] Downloading blob %s...\n", digestStr[:19])
	}

	// Get compressed blob from registry
	compressed, err := layer.Compressed()
	if err != nil {
		return DownloadResult{Err: fmt.Errorf("failed to get compressed layer: %w", err)}
	}
	defer compressed.Close()

	// Get size
	size, err := layer.Size()
	if err != nil {
		return DownloadResult{Err: fmt.Errorf("failed to get layer size: %w", err)}
	}

	// Download and cache blob (with digest verification inside Put)
	if err := bd.blobCache.Put(digestStr, diffIDStr, compressed, imageRef); err != nil {
		return DownloadResult{Err: fmt.Errorf("failed to cache blob: %w", err)}
	}

	if bd.debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] Blob %s downloaded and cached (%d bytes)\n", digestStr[:19], size)
	}

	return DownloadResult{
		Digest:    digestStr,
		DiffID:    diffIDStr,
		Size:      size,
		FromCache: false,
	}
}

// DownloadProgressCallback is called with progress updates
type DownloadProgressCallback func(completed, total int, currentBlob string)

// DownloadBlobsWithProgress downloads blobs with progress reporting
func (bd *BlobDownloader) DownloadBlobsWithProgress(
	ctx context.Context,
	layers []v1.Layer,
	imageRef string,
	maxConcurrency int,
	progressCallback DownloadProgressCallback,
) ([]DownloadResult, error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}

	results := make([]DownloadResult, len(layers))
	var wg sync.WaitGroup
	var completed int
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)

	for i, layer := range layers {
		wg.Add(1)
		go func(index int, l v1.Layer) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check if cancelled
			select {
			case <-ctx.Done():
				results[index] = DownloadResult{Err: ctx.Err()}
				return
			default:
			}

			// Get digest for progress reporting
			digest, _ := l.Digest()
			digestStr := ""
			if digest.String() != "" {
				digestStr = digest.String()
			}

			// Download blob
			result := bd.downloadSingleBlob(ctx, l, imageRef)
			results[index] = result

			// Update progress
			mu.Lock()
			completed++
			current := completed
			mu.Unlock()

			if progressCallback != nil {
				progressCallback(current, len(layers), digestStr)
			}
		}(i, layer)
	}

	wg.Wait()

	// Check for errors
	for i, result := range results {
		if result.Err != nil {
			return results, fmt.Errorf("failed to download layer %d: %w", i, result.Err)
		}
	}

	return results, nil
}

// GetCachedBlobReader returns a reader for a cached blob
func (bd *BlobDownloader) GetCachedBlobReader(digest string) (io.ReadCloser, error) {
	return bd.blobCache.Get(digest)
}

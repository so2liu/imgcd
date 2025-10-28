package diff

import (
	"context"
	"fmt"
	"os"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/so2liu/imgcd/internal/remote"
)

// LayerStatus represents the status of a layer in the diff
type LayerStatus string

const (
	LayerStatusNew    LayerStatus = "NEW"
	LayerStatusShared LayerStatus = "SHARED"
)

// LayerDiff represents the difference information for a single layer
type LayerDiff struct {
	DiffID  v1.Hash
	Digest  v1.Hash
	Size    int64
	Command string
	Status  LayerStatus
}

// DiffResult contains the result of comparing two images
type DiffResult struct {
	NewImage     *remote.ImageMetadata
	BaseImage    *remote.ImageMetadata
	LayerDiffs   []LayerDiff
	NewLayers    []LayerDiff
	SharedLayers []LayerDiff

	// Size statistics
	NewLayersSize     int64
	SharedLayersSize  int64
	TotalNewImageSize int64
	SavingsSize       int64
	SavingsPercentage float64
}

// Differ compares two container images
type Differ struct {
	fetcher *remote.Fetcher
}

// NewDiffer creates a new Differ
func NewDiffer(fetcher *remote.Fetcher) *Differ {
	return &Differ{
		fetcher: fetcher,
	}
}

// Compare compares two images and returns the differences
func (d *Differ) Compare(ctx context.Context, newImageRef, baseImageRef, platform string) (*DiffResult, error) {
	debug := os.Getenv("IMGCD_DEBUG") != ""
	startTime := time.Now()

	if debug {
		fmt.Fprintf(os.Stderr, "\n[DEBUG] === Starting image comparison ===\n")
		fmt.Fprintf(os.Stderr, "[DEBUG] Fetching both images in parallel...\n")
	}

	// Fetch metadata for both images in parallel
	type fetchResult struct {
		metadata *remote.ImageMetadata
		err      error
		duration time.Duration
		name     string
	}

	results := make(chan fetchResult, 2)

	// Fetch new image in goroutine
	go func() {
		t1 := time.Now()
		metadata, err := d.fetcher.FetchImageMetadata(ctx, newImageRef, platform)
		results <- fetchResult{
			metadata: metadata,
			err:      err,
			duration: time.Since(t1),
			name:     "new image",
		}
	}()

	// Fetch base image in goroutine
	go func() {
		t2 := time.Now()
		metadata, err := d.fetcher.FetchImageMetadata(ctx, baseImageRef, platform)
		results <- fetchResult{
			metadata: metadata,
			err:      err,
			duration: time.Since(t2),
			name:     "base image",
		}
	}()

	// Collect results
	var newImage, baseImage *remote.ImageMetadata
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			return nil, fmt.Errorf("failed to fetch %s metadata: %w", result.name, result.err)
		}
		if debug {
			fmt.Fprintf(os.Stderr, "[DEBUG] Fetch %s: %v\n", result.name, result.duration)
		}

		// Assign to correct variable based on reference
		if result.metadata.Reference == newImageRef {
			newImage = result.metadata
		} else {
			baseImage = result.metadata
		}
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] Parallel fetch completed: %v\n", time.Since(startTime))
	}

	// Build a map of base image layer DiffIDs for quick lookup
	t3 := time.Now()
	baseLayerMap := make(map[string]bool, len(baseImage.Layers))
	for _, layer := range baseImage.Layers {
		baseLayerMap[layer.DiffID.String()] = true
	}

	// Compare layers
	var layerDiffs []LayerDiff
	var newLayers []LayerDiff
	var sharedLayers []LayerDiff
	var newLayersSize int64
	var sharedLayersSize int64

	for _, layer := range newImage.Layers {
		diff := LayerDiff{
			DiffID:  layer.DiffID,
			Digest:  layer.Digest,
			Size:    layer.Size,
			Command: layer.Command,
		}

		if baseLayerMap[layer.DiffID.String()] {
			// This layer exists in the base image
			diff.Status = LayerStatusShared
			sharedLayers = append(sharedLayers, diff)
			sharedLayersSize += layer.Size
		} else {
			// This is a new layer
			diff.Status = LayerStatusNew
			newLayers = append(newLayers, diff)
			newLayersSize += layer.Size
		}

		layerDiffs = append(layerDiffs, diff)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] Compare and calculate: %v\n", time.Since(t3))
	}

	// Calculate savings
	totalNewImageSize := newImage.TotalSize
	savingsSize := sharedLayersSize
	savingsPercentage := 0.0
	if totalNewImageSize > 0 {
		savingsPercentage = float64(savingsSize) / float64(totalNewImageSize) * 100.0
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] === Total comparison time: %v ===\n\n", time.Since(startTime))
	}

	return &DiffResult{
		NewImage:          newImage,
		BaseImage:         baseImage,
		LayerDiffs:        layerDiffs,
		NewLayers:         newLayers,
		SharedLayers:      sharedLayers,
		NewLayersSize:     newLayersSize,
		SharedLayersSize:  sharedLayersSize,
		TotalNewImageSize: totalNewImageSize,
		SavingsSize:       savingsSize,
		SavingsPercentage: savingsPercentage,
	}, nil
}

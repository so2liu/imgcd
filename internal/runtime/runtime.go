package runtime

import (
	"context"
	"io"
)

// Runtime represents a container runtime interface
type Runtime interface {
	// Name returns the runtime name (docker, containerd, etc.)
	Name() string

	// GetImage retrieves image information
	GetImage(ctx context.Context, ref string) (*ImageInfo, error)

	// SaveImage saves an image to a file
	SaveImage(ctx context.Context, ref, outputPath string) error

	// LoadImage loads an image from a file
	LoadImage(ctx context.Context, inputPath string) error

	// LoadImageFromReader loads an image from a reader
	LoadImageFromReader(ctx context.Context, r io.Reader) error

	// Close closes the runtime client
	Close() error
}

// ImageInfo contains essential image information
type ImageInfo struct {
	Reference string
	ID        string
	Layers    []LayerInfo
	RepoTags  []string
}

// LayerInfo contains information about a layer
type LayerInfo struct {
	Digest    string
	Size      int64
	MediaType string
	Exists    bool
}

// DetectRuntime tries to detect available container runtime
func DetectRuntime() (Runtime, error) {
	// Try Docker first
	if rt, err := NewDockerRuntime(); err == nil {
		return rt, nil
	}

	// Try containerd
	if rt, err := NewContainerdRuntime(); err == nil {
		return rt, nil
	}

	return nil, ErrNoRuntimeAvailable
}

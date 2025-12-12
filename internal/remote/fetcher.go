package remote

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImageMetadata contains metadata about a container image fetched from a registry
type ImageMetadata struct {
	Reference  string
	Platform   string
	Digest     v1.Hash
	Layers     []LayerMetadata
	TotalSize  int64
	ConfigFile *v1.ConfigFile
}

// LayerMetadata contains information about a single image layer
type LayerMetadata struct {
	DiffID  v1.Hash
	Digest  v1.Hash
	Size    int64
	Command string // The Docker command that created this layer
}

// Fetcher handles fetching image metadata from remote registries
type Fetcher struct {
	options []remote.Option
}

// NewFetcher creates a new Fetcher with the given options
func NewFetcher(opts ...remote.Option) *Fetcher {
	return &Fetcher{
		options: opts,
	}
}

// FetchImageMetadata retrieves image metadata from a remote registry without downloading layers
func (f *Fetcher) FetchImageMetadata(ctx context.Context, imageRef string, platformSpec string) (*ImageMetadata, error) {
	debug := os.Getenv("IMGCD_DEBUG") != ""
	startTime := time.Now()

	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] Fetching metadata for %s (%s)\n", imageRef, platformSpec)
	}

	// Parse the image reference
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse image reference %q: %w", imageRef, err)
	}

	// Parse platform specification
	platform, err := v1.ParsePlatform(platformSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to parse platform %q: %w", platformSpec, err)
	}

	// Build remote options with platform and authentication
	// Use DefaultKeychain to automatically read Docker credentials from ~/.docker/config.json
	opts := append(f.options,
		remote.WithContext(ctx),
		remote.WithPlatform(*platform),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)

	// Fetch the image descriptor (manifest and config only, no layers)
	t1 := time.Now()
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image descriptor: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG]   remote.Get: %v\n", time.Since(t1))
	}

	// Get the image from the descriptor
	t2 := time.Now()
	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("failed to get image from descriptor: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG]   desc.Image: %v\n", time.Since(t2))
	}

	// Get the image digest
	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("failed to get image digest: %w", err)
	}

	// Get the config file
	t3 := time.Now()
	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("failed to get config file: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG]   img.ConfigFile: %v\n", time.Since(t3))
	}

	// Get layers
	t4 := time.Now()
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("failed to get layers: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG]   img.Layers: %v\n", time.Since(t4))
	}

	// Extract layer metadata
	t5 := time.Now()
	layerMetadata := make([]LayerMetadata, 0, len(layers))
	var totalSize int64

	for i, layer := range layers {
		diffID, err := layer.DiffID()
		if err != nil {
			return nil, fmt.Errorf("failed to get layer DiffID: %w", err)
		}

		layerDigest, err := layer.Digest()
		if err != nil {
			return nil, fmt.Errorf("failed to get layer digest: %w", err)
		}

		size, err := layer.Size()
		if err != nil {
			return nil, fmt.Errorf("failed to get layer size: %w", err)
		}

		totalSize += size

		// Extract the command from history (if available)
		command := ""
		if i < len(configFile.History) {
			if configFile.History[i].CreatedBy != "" {
				command = configFile.History[i].CreatedBy
			}
		}

		layerMetadata = append(layerMetadata, LayerMetadata{
			DiffID:  diffID,
			Digest:  layerDigest,
			Size:    size,
			Command: command,
		})
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[DEBUG]   Extract layer metadata (%d layers): %v\n", len(layers), time.Since(t5))
		fmt.Fprintf(os.Stderr, "[DEBUG] Total fetch time for %s: %v\n", imageRef, time.Since(startTime))
	}

	return &ImageMetadata{
		Reference:  imageRef,
		Platform:   platformSpec,
		Digest:     digest,
		Layers:     layerMetadata,
		TotalSize:  totalSize,
		ConfigFile: configFile,
	}, nil
}

// ListTags lists all tags for a given repository
func (f *Fetcher) ListTags(ctx context.Context, repository string) ([]string, error) {
	repo, err := name.NewRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository %q: %w", repository, err)
	}

	opts := append(f.options,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)

	tags, err := remote.List(repo, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	return tags, nil
}

// ResolveTag resolves a tag input to an exact tag.
// Priority:
// 1. Exact match - if tag exists as-is, return it
// 2. Fuzzy match - find tags containing the input
//   - If exactly one match, return it
//   - If multiple matches, return ("", matches, nil) for user selection
//   - If no matches, return error
func (f *Fetcher) ResolveTag(ctx context.Context, repository, tagInput string) (string, []string, error) {
	tags, err := f.ListTags(ctx, repository)
	if err != nil {
		return "", nil, err
	}

	// 1. Try exact match first
	for _, tag := range tags {
		if tag == tagInput {
			return tag, nil, nil // Exact match found
		}
	}

	// 2. Fuzzy match - find tags containing the input
	var matches []string
	for _, tag := range tags {
		if strings.Contains(tag, tagInput) {
			matches = append(matches, tag)
		}
	}

	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("no tags found matching %q in %s", tagInput, repository)
	case 1:
		return matches[0], nil, nil // Single fuzzy match
	default:
		return "", matches, nil // Multiple matches - need user selection
	}
}

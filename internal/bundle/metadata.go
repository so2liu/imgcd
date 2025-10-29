package bundle

import (
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Metadata represents the metadata for imgcd bundle format
// This format stores registry blobs directly (compressed) instead of
// decompressing layers, significantly improving save performance
type Metadata struct {
	// Version is the bundle format version
	Version string `json:"version"`

	// ImageRef is the full reference of the image (e.g., "alpine:3.20")
	ImageRef string `json:"image_ref"`

	// BaseRef is the reference used for incremental export (e.g., "alpine:3.19")
	// Empty if this is a full export
	BaseRef string `json:"base_ref,omitempty"`

	// SharedLayerCount is the number of layers shared with base image
	// Used during incremental import to know how many layers to get from base
	SharedLayerCount int `json:"shared_layer_count,omitempty"`

	// Platform is the target platform (e.g., "linux/amd64")
	Platform string `json:"platform"`

	// Manifest is the OCI/Docker manifest
	Manifest *v1.Manifest `json:"manifest"`

	// Config is the image config
	Config *v1.ConfigFile `json:"config"`

	// Layers contains the mapping between digest (compressed) and diffid (uncompressed)
	// This is crucial for Load to verify layers and rebuild image.tar
	Layers []LayerInfo `json:"layers"`

	// TotalSize is the total compressed size of all layers in bytes
	TotalSize int64 `json:"total_size"`

	// CreatedAt is the timestamp when this bundle was created
	CreatedAt string `json:"created_at"`
}

// LayerInfo contains information about a single layer in the bundle
type LayerInfo struct {
	// Digest is the compressed layer's SHA256 (this is the blob filename)
	Digest string `json:"digest"`

	// DiffID is the uncompressed layer's SHA256 (used for verification in Load)
	DiffID string `json:"diffid"`

	// Size is the compressed size in bytes
	Size int64 `json:"size"`

	// UncompressedSize is the uncompressed size in bytes
	UncompressedSize int64 `json:"uncompressed_size,omitempty"`

	// MediaType is the layer media type (e.g., "application/vnd.docker.image.rootfs.diff.tar.gzip")
	MediaType string `json:"media_type,omitempty"`
}

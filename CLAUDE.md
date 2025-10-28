# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test Commands

```bash
# Build binary
make build                    # Builds imgcd binary
go build -o ./dist/imgcd ./cmd/imgcd # Direct build

# Testing and code quality
make test                     # Run all tests
go test -v ./...             # Run tests with verbose output
make fmt                      # Format code
make vet                      # Run go vet
make check                    # Run fmt + vet + test

# Clean build artifacts
make clean                    # Remove binary and output directories
```

## Architecture

imgcd is a CLI tool for incremental container image export/import, designed for offline environments.

### Core Components

**Runtime Abstraction (internal/runtime/)**

-   `Runtime` interface provides unified API for Docker and containerd
-   `DetectRuntime()` auto-detects available container runtime
-   Key operations: GetImage, GetImageWithPlatform (auto-pull), SaveImage, LoadImage
-   Platform-aware: pulls images for target platform, not current platform

**Image Export/Import (internal/image/)**

-   `Exporter`: Orchestrates export process with incremental layer filtering
-   `RemoteExporter`: Exports images directly from registry using blob-based caching (zero decompression)
-   `BundleGenerator`: Creates self-extracting shell scripts (.sh bundles)
-   `BundleLoader`: Reconstructs Docker image.tar from compressed blobs on target system
-   `incremental.go`: True incremental export - filters out shared layers between base and target images using DiffID comparison
-   Uses google/go-containerregistry for image metadata and layer handling

**CLI (internal/cli/)**

-   Cobra-based command structure: save, load, diff, update
-   `save`: Export image with optional --since for incremental exports
-   `diff`: Compare images using metadata only (no layer downloads), useful for estimating incremental export sizes
-   Version injection: Version variable set by main.go at runtime from git tag

**Remote/Diff (internal/remote/, internal/diff/)**

-   `Fetcher`: Downloads image metadata (manifests, configs) from registries without pulling layers
-   `BlobDownloader`: Downloads compressed blobs in parallel with digest verification
-   `Differ`: Compares layer DiffIDs between images to show what would be included in incremental export
-   Supports JSON and text output formats with optional verbose mode
-   Platform-aware: fetches metadata for specified target platform

**Blob Cache (internal/cache/)**

-   `BlobCache`: Manages local cache of compressed registry blobs
-   Stores blobs by digest (compressed SHA256) for efficient reuse
-   Zero decompression during save - blobs stored in original format
-   Cross-image deduplication via shared blob cache
-   Cache structure: `~/.imgcd/cache/blobs/sha256/{digest}` + `index.json`

**Bundle Format (internal/bundle/)**

-   `Metadata`: Bundle metadata including digest↔diffid mapping
-   `LayerInfo`: Layer information with both compressed (digest) and uncompressed (diffid) hashes
-   Bundle structure: `metadata.json` + `blobs/sha256/{digest}`

### Key Design Patterns

1. **Self-Extracting Bundles**:

    - Embeds imgcd binary (for target platform) + image data into a single .sh file
    - Target system doesn't need imgcd installed
    - Binary cache: ~/.imgcd/bin/{version}/{platform}/imgcd
    - Dev mode: uses IMGCD_BINARY_PATH or current binary if platform matches

2. **Blob-based Caching**:

    - Downloads compressed blobs directly from registry (no decompression)
    - Stores blobs by digest in `~/.imgcd/cache/blobs/`
    - Save stage: zero decompression, constant memory usage (~50MB)
    - Load stage: decompresses and verifies blobs, rebuilds Docker format
    - Significant performance improvement: 50-80% faster with cache

3. **Incremental Export**:

    - Compares layer DiffIDs (uncompressed digests) between new and base images
    - Filters out shared layers from export
    - Only downloads/packages new layers
    - Falls back to full export if all layers match

4. **Platform-Aware Pulling**:

    - `--target-platform` (default: linux/amd64) determines which platform image to pull
    - Automatically pulls images for target platform even if running on different platform
    - Example: `imgcd save alpine -t linux/arm64` on macOS pulls linux/arm64 variant

5. **Reference Normalization**:
    - `--since` flag accepts both full refs (alpine:3.19) and short tags (3.19)
    - Short tags automatically use same repository as target image

## Version Management and Releases

-   Version is injected via ldflags during build: `-X main.version=v0.3.1`
-   In development, version defaults to "dev"
-   **IMPORTANT**: Do NOT create git tags unless explicitly requested - tags trigger releases
-   Release process: `git tag -a v0.3.1 -m "Brief release message"` then `git push --tags`
-   Release workflow (`.github/workflows/release.yml`) builds for all platforms on tag push

## File Naming Convention

Output files follow pattern: `{repo}_{tag}__since-{base_tag}.sh`

-   Repository slashes and colons replaced with underscores
-   Example: `alpine-3.20__since-3.19.sh`
-   Example: `myrepo_app-2.0.0__since-1.9.0.sh`

## Diff Command

The `diff` command compares two images by fetching only metadata (manifests and configs) without downloading layer data. This is useful for quickly estimating incremental export sizes before performing actual exports.

```bash
# Compare alpine versions (short tag format)
imgcd diff alpine:3.20 --since 3.19

# Verbose output with layer details
imgcd diff alpine:3.20 --since 3.19 --verbose

# JSON output for scripting
imgcd diff alpine:3.20 --since 3.19 --output json

# Specify target platform
imgcd diff myapp:2.0 --since 1.9 -t linux/arm64
```

## Layer Caching

Remote mode automatically caches downloaded layers at `~/.imgcd/cache/` to avoid re-downloading. This significantly speeds up repeated exports and exports of images with shared layers.

**Cache behavior:**
- Enabled by default in remote mode
- Disabled in local mode (not needed - runtime already optimizes)
- Use `--no-cache` flag to disable caching for a specific export

**Cache management commands:**

```bash
# List all cached layers with source images
imgcd cache list

# Show cache statistics (size, hit rate, etc.)
imgcd cache info

# Remove old layers (default: 30 days)
imgcd cache prune
imgcd cache prune --days 60

# Clean all cache
imgcd cache clean
imgcd cache clean --force  # Skip confirmation
```

**Cache structure:**
```
~/.imgcd/
├── bin/              # Binary cache (release mode downloads)
└── cache/
    ├── blobs/        # Blob cache (compressed registry blobs)
    │   └── sha256/
    │       └── {digest}  # Original compressed blob
    └── index.json    # Blob metadata (digest→diffid mapping, image refs)
```

**Performance benefits:**
- First export: Downloads and caches compressed blobs (no decompression)
- Repeated export: 50-80% faster (directly packs cached blobs)
- Incremental export: Only downloads new blobs, reuses cached base blobs
- Cross-image reuse: Shared blobs between different images are cached once
- Memory efficient: Constant ~50MB memory usage regardless of image size

**Example workflow:**
```bash
# First time: downloads and caches blobs
imgcd save postgres:15
# Cache hits: 0/14 blobs

# Second time: much faster (all blobs cached)
imgcd save postgres:15
# Cache hits: 14/14 blobs

# Incremental: only downloads new blobs
imgcd save postgres:16 --since postgres:15
# Cache hits: 12/14 blobs (2 new layers)
```

## Testing in Development Mode

During development (version="dev"), binary packaging behavior:

```bash
# Same platform: Uses current binary
./imgcd save alpine -t darwin/arm64  # On macOS ARM64

# Cross-platform: Uses current binary with warning
./imgcd save alpine -t linux/amd64   # On macOS ARM64
# Warning: This bundle will only work on darwin/arm64 systems

# Production testing: Use IMGCD_BINARY_PATH for correct platform binary
GOOS=linux GOARCH=amd64 go build -o imgcd-linux-amd64 ./cmd/imgcd
IMGCD_BINARY_PATH=./imgcd-linux-amd64 ./imgcd save alpine -t linux/amd64
```

## Architecture Notes

**Save vs Load Separation:**
- Save (development machine): Lightweight, downloads compressed blobs, zero decompression
- Load (target server): Handles all decompression and Docker format reconstruction
- This design optimizes for fast saves on developer machines and utilizes server CPU for heavy work

## Dependencies

-   Go 1.24 (specified in go.mod)
-   github.com/spf13/cobra: CLI framework
-   github.com/google/go-containerregistry: Image parsing and layer extraction
-   github.com/rhysd/go-github-selfupdate: Self-update functionality

## User's Extra Requirements

-   除非用户明确要求，否则不要轻易 tag，因为这样会导致发版
-   通过 git tag -a v0.3.1 -m "Release message" 的方式发版，认真写超级简短的 release message
- 除非用户明确要求，否则发版只发最小版本号
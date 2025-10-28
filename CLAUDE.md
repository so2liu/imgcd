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
-   `BundleGenerator`: Creates self-extracting shell scripts (.sh bundles)
-   `incremental.go`: True incremental export - filters out shared layers between base and target images using DiffID comparison
-   Uses google/go-containerregistry to parse Docker image tars and extract layer information

**CLI (internal/cli/)**

-   Cobra-based command structure: save, load, diff, update
-   `save`: Export image with optional --since for incremental exports
-   `diff`: Compare images using metadata only (no layer downloads), useful for estimating incremental export sizes
-   Version injection: Version variable set by main.go at runtime from git tag

**Remote/Diff (internal/remote/, internal/diff/)**

-   `Fetcher`: Downloads image metadata (manifests, configs) from registries without pulling layers
-   `Differ`: Compares layer DiffIDs between images to show what would be included in incremental export
-   Supports JSON and text output formats with optional verbose mode
-   Platform-aware: fetches metadata for specified target platform

### Key Design Patterns

1. **Self-Extracting Bundles**:

    - Embeds imgcd binary (for target platform) + image data into a single .sh file
    - Target system doesn't need imgcd installed
    - Binary cache: ~/.imgcd/bin/{version}/{platform}/imgcd
    - Dev mode: uses IMGCD_BINARY_PATH or current binary if platform matches

2. **Incremental Export**:

    - Compares layer DiffIDs (uncompressed digests) between new and base images
    - Filters out shared layers from export
    - Maintains Docker image tar format (manifest.json, config, layers)
    - Falls back to full export if all layers match

3. **Platform-Aware Pulling**:

    - `--target-platform` (default: linux/amd64) determines which platform image to pull
    - Automatically pulls images for target platform even if running on different platform
    - Example: `imgcd save alpine -t linux/arm64` on macOS pulls linux/arm64 variant

4. **Reference Normalization**:
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
├── bin/              # Binary cache (existing)
└── cache/
    ├── layers/       # Layer cache
    │   └── sha256/
    │       └── {short-diffid}/
    │           └── layer.tar.gz
    └── metadata.json # Layer metadata (image ref, timestamps)
```

**Performance benefits:**
- First export: Downloads and caches layers
- Repeated export: Instant (reads from cache)
- Incremental export: Only downloads new layers, reuses cached base layers
- Cross-image reuse: Shared layers between different images are cached once

**Example workflow:**
```bash
# First time: downloads and caches
imgcd save postgres:15

# Second time: instant (all layers cached)
imgcd save postgres:15 --since postgres:14

# Different image sharing layers: reuses cache
imgcd save postgres:16 --since postgres:15  # Only new layers downloaded
```

## Testing Incremental Export Locally

During development (version="dev"), cross-platform bundles require:

```bash
# Option 1: Build for target platform and set IMGCD_BINARY_PATH
GOOS=linux GOARCH=arm64 go build -o imgcd-linux-arm64 ./cmd/imgcd
IMGCD_BINARY_PATH=./imgcd-linux-arm64 ./imgcd save alpine -t linux/arm64

# Option 2: Match target platform to current platform
./imgcd save alpine -t darwin/arm64  # If on macOS ARM64
```

## Dependencies

-   Go 1.24 (specified in go.mod)
-   github.com/spf13/cobra: CLI framework
-   github.com/google/go-containerregistry: Image parsing and layer extraction
-   github.com/rhysd/go-github-selfupdate: Self-update functionality

## User's Extra Requirements

-   除非用户明确要求，否则不要轻易 tag，因为这样会导致发版
-   通过 git tag -a v0.3.1 -m "Release message" 的方式发版，认真写超级简短的 release message

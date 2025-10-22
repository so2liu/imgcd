# imgcd

A lightweight CLI tool for exporting and importing container images with support for incremental/differential exports. Designed for offline environments where images need to be transferred via physical media (CD, USB, etc.).

## Features

- **Self-Extracting Bundles**: Creates standalone `.sh` files that include the imgcd binary - no installation needed on target systems!
- **Incremental Export**: Only export layers that differ from a base image, reducing transfer size
- **Simple CLI**: Just two commands - `save` and `load`
- **Auto-detection**: Automatically detects and uses Docker or containerd
- **Auto-pull**: Automatically pulls images from registry if not found locally
- **Target Platform Selection**: Choose the target platform (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)
- **Cross-platform**: Supports macOS and Linux

## Installation

### Pre-built Binary (Recommended)

Download the latest release for your platform from the [releases page](https://github.com/so2liu/imgcd/releases).

**Linux (amd64):**
```bash
wget https://github.com/so2liu/imgcd/releases/latest/download/imgcd-linux-amd64.tar.gz
tar -xzf imgcd-linux-amd64.tar.gz
sudo mv imgcd-linux-amd64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**Linux (arm64):**
```bash
wget https://github.com/so2liu/imgcd/releases/latest/download/imgcd-linux-arm64.tar.gz
tar -xzf imgcd-linux-arm64.tar.gz
sudo mv imgcd-linux-arm64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**macOS (Intel):**
```bash
curl -L https://github.com/so2liu/imgcd/releases/latest/download/imgcd-darwin-amd64.tar.gz -o imgcd-darwin-amd64.tar.gz
tar -xzf imgcd-darwin-amd64.tar.gz
sudo mv imgcd-darwin-amd64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**macOS (Apple Silicon):**
```bash
curl -L https://github.com/so2liu/imgcd/releases/latest/download/imgcd-darwin-arm64.tar.gz -o imgcd-darwin-arm64.tar.gz
tar -xzf imgcd-darwin-arm64.tar.gz
sudo mv imgcd-darwin-arm64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

### From Source

Requires Go 1.22 or later:

```bash
git clone https://github.com/so2liu/imgcd.git
cd imgcd
go build -o imgcd ./cmd/imgcd
sudo mv imgcd /usr/local/bin/
```

## Usage

### Export an Image

`imgcd save` creates a **self-extracting bundle** - a standalone shell script that contains both the imgcd binary and your image data. This means the target system doesn't need imgcd installed!

**Simplest form:**
```bash
# Export alpine (defaults to linux/amd64)
imgcd save alpine
# Output: ./out/alpine-latest__since-none.sh
```

**Full export with tag:**
```bash
# Export entire image as self-extracting bundle
imgcd save ns/app:1.0.0
# Output: ./out/ns_app-1.0.0__since-none.sh
```

**Incremental export:**

> **Tip**: The `--since` flag accepts either a full image reference or just a tag. When using just a tag, it automatically uses the same repository as the target image.

```bash
# Incremental export with short tag format (recommended)
imgcd save alpine:3.20 --since 3.19
# Output: ./out/alpine-3.20__since-3.19.sh

imgcd save myrepo/app:2.0.0 --since 1.9.0
# Output: ./out/myrepo_app-2.0.0__since-1.9.0.sh
```

**Specify target platform:**
```bash
# For Linux ARM64
imgcd save myapp:v2.0 --target-platform linux/arm64
imgcd save myapp:v2.0 -t linux/arm64

# For macOS Apple Silicon
imgcd save myapp:v2.0 -t darwin/arm64
```

> **Important**: imgcd automatically pulls images for the target platform. For example, running `imgcd save alpine -t linux/amd64` on macOS ARM64 will pull the linux/amd64 version of alpine, ensuring the exported bundle works correctly on the target system.

**Real-world example:**
```bash
# Full export: creates a self-extracting bundle
imgcd save myapp:v2.0

# Incremental export: 20% smaller!
imgcd save myapp:v2.0 --since v1.9
# Output shows: Filtered 8/13 layers (saved 23.9 MB)
```

**Custom output directory:**
```bash
imgcd save ns/app:2.0.0 --out-dir /tmp/bundles
# Output: /tmp/bundles/ns_app-2.0.0__since-none.sh
```

### Import an Image

**On the target system (no imgcd installation needed!):**
```bash
# Just run the self-extracting bundle
chmod +x ./alpine-latest__since-none.sh
./alpine-latest__since-none.sh
```

## How It Works

1. **Save**:
   - Detects available container runtime (Docker or containerd)
   - Automatically pulls images for the target platform
   - Exports the image using `docker save` or `ctr export`
   - **Compares with base image and filters out shared layers** (true incremental export!)
   - Downloads the imgcd binary for the target platform (cached in `~/.imgcd/bin/`)
   - Creates a self-extracting shell script (`.sh`) with embedded binary and image data

2. **Load** (run the bundle on target):
   - Detects current platform and validates compatibility
   - Extracts embedded imgcd binary and image data to temporary directory
   - Runs the import automatically
   - Cleans up temporary files

### Auto-Pull Feature

When you export an image that doesn't exist locally, `imgcd` will automatically pull it from the registry:

```bash
# Even if alpine:3.20 is not local, it will be pulled automatically
imgcd save alpine:3.20

# Output:
# Image alpine:3.20 not found locally, pulling...
# 3.20: Pulling from library/alpine
# ...
```

This also works for the base image specified with `--since`:

```bash
# Both images will be pulled if not found locally
imgcd save myapp:2.0 --since myapp:1.0
```

## Requirements

**On the exporting system (where you run `imgcd save`):**
- Docker or containerd must be installed and running
- For Docker: `docker` CLI must be available
- For containerd: `ctr` CLI must be available
- Internet access (to download imgcd binaries for target platforms, cached after first use)

**On the target system (where you import):**
- Docker or containerd must be installed and running
- **No imgcd installation needed** when using self-extracting bundles!

## Architecture

```
imgcd/
├── cmd/imgcd/          # CLI entry point
├── internal/
│   ├── cli/            # Command implementations
│   ├── runtime/        # Docker/containerd abstraction
│   ├── image/          # Export/import logic
│   └── archive/        # Archive packing/unpacking
```

## Roadmap

- [x] Basic save/load functionality
- [x] Runtime auto-detection (Docker/containerd)
- [x] Metadata embedding in archives
- [x] **True incremental layer filtering** - saves 20-50% size on incremental exports!
- [x] Auto-pull missing images
- [x] Short tag format for --since flag
- [x] **Self-extracting bundles** - no installation needed on target systems!
- [x] Target platform selection
- [ ] Progress indicators
- [ ] Compression level options
- [ ] Support for additional runtimes (podman, etc.)
- [ ] Checksum validation

## License

MIT License - see LICENSE file for details

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

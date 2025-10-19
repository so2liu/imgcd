# imgcd

A lightweight CLI tool for exporting and importing container images with support for incremental/differential exports. Designed for offline environments where images need to be transferred via physical media (CD, USB, etc.).

## Features

- **Incremental Export**: Only export layers that differ from a base image, reducing transfer size
- **Simple CLI**: Just two commands - `save` and `load`
- **Auto-detection**: Automatically detects and uses Docker or containerd
- **Auto-pull**: Automatically pulls images from registry if not found locally
- **Portable Archives**: Output is a single `.tar.gz` file with embedded metadata
- **Cross-platform**: Supports macOS and Linux

## Installation

### Pre-built Binary (Recommended)

Download the latest release for your platform from the [releases page](https://github.com/yangliu35/imgcd/releases).

**Linux (amd64):**
```bash
wget https://github.com/yangliu35/imgcd/releases/latest/download/imgcd-linux-amd64.tar.gz
tar -xzf imgcd-linux-amd64.tar.gz
sudo mv imgcd-linux-amd64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**Linux (arm64):**
```bash
wget https://github.com/yangliu35/imgcd/releases/latest/download/imgcd-linux-arm64.tar.gz
tar -xzf imgcd-linux-arm64.tar.gz
sudo mv imgcd-linux-arm64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**macOS (Intel):**
```bash
curl -L https://github.com/yangliu35/imgcd/releases/latest/download/imgcd-darwin-amd64.tar.gz -o imgcd-darwin-amd64.tar.gz
tar -xzf imgcd-darwin-amd64.tar.gz
sudo mv imgcd-darwin-amd64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

**macOS (Apple Silicon):**
```bash
curl -L https://github.com/yangliu35/imgcd/releases/latest/download/imgcd-darwin-arm64.tar.gz -o imgcd-darwin-arm64.tar.gz
tar -xzf imgcd-darwin-arm64.tar.gz
sudo mv imgcd-darwin-arm64 /usr/local/bin/imgcd
chmod +x /usr/local/bin/imgcd
```

### From Source

Requires Go 1.22 or later:

```bash
git clone https://github.com/yangliu35/imgcd.git
cd imgcd
go build -o imgcd ./cmd/imgcd
sudo mv imgcd /usr/local/bin/
```

## Usage

### Export an Image

**Full export:**
```bash
# Export entire image
imgcd save ns/app:1.0.0
# Output: ./out/ns_app-1.0.0__since-none.tar.gz
```

**Incremental export:**

> 💡 **Tip**: The `--since` flag accepts either a full image reference or just a tag. When using just a tag, it automatically uses the same repository as the target image.

```bash
# Full reference format
imgcd save ns/app:1.2.9 --since ns/app:1.2.8
# Output: ./out/ns_app-1.2.9__since-1.2.8.tar.gz

# Short tag format (recommended for same repository)
imgcd save alpine:3.20 --since 3.19
imgcd save myrepo/app:2.0.0 --since 1.9.0
# Output: ./out/alpine-3.20__since-3.19.tar.gz
```

**Real-world example:**
```bash
# Full export: 103MB
imgcd save myapp:v2.0

# Incremental export: 82MB (20% smaller!)
imgcd save myapp:v2.0 --since v1.9
# Output shows: Filtered 8/13 layers (saved 23.9 MB)
```

**Custom output directory:**
```bash
imgcd save ns/app:2.0.0 --since ns/app:1.9.0 --out-dir /tmp/bundles
# Output: /tmp/bundles/ns_app-2.0.0__since-1.9.0.tar.gz
```

### Import an Image

```bash
# Import from tar.gz (image name/tag auto-detected)
imgcd load --from ./out/ns_app-1.2.9__since-1.2.8.tar.gz
```

## How It Works

1. **Save**:
   - Detects available container runtime (Docker or containerd)
   - Automatically pulls images if not found locally
   - Exports the image using `docker save` or `ctr export`
   - **Compares with base image and filters out shared layers** (true incremental export!)
   - Creates a `.tar.gz` archive with metadata and only new layers

2. **Load**:
   - Extracts metadata and image from the archive
   - Imports using `docker load` or `ctr import`
   - Skips layers that already exist in the runtime

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

- Docker or containerd must be installed and running
- For Docker: `docker` CLI must be available
- For containerd: `ctr` CLI must be available

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
- [ ] Progress indicators
- [ ] Compression level options
- [ ] Support for additional runtimes (podman, etc.)
- [ ] Checksum validation

## License

MIT License - see LICENSE file for details

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

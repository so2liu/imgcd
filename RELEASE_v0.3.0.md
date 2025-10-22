# v0.3.0 - Self-Extracting Bundles

## What's New

- **Self-extracting bundles**: Output is now a standalone `.sh` file with embedded imgcd binary - no installation needed on target systems
- **Platform-aware pulling**: Automatically pulls images for target platform (e.g., pull linux/amd64 on macOS ARM64)
- **Simplified CLI**: Removed `--tar-only`, simplest usage is just `imgcd save alpine`

## Usage

```bash
# Export (defaults to linux/amd64)
imgcd save alpine

# Import on target (no imgcd needed!)
chmod +x alpine-latest__since-none.sh
./alpine-latest__since-none.sh
```

## Breaking Changes

- Removed `--tar-only` flag
- Output format changed from `.tar.gz` to `.sh`

**Full Changelog**: https://github.com/so2liu/imgcd/compare/v0.2.1...v0.3.0

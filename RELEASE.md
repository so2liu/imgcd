# Release Guide

This document describes how to create a new release of `imgcd`.

## Prerequisites

- Push access to the GitHub repository
- All changes committed and pushed to main branch
- CI passing on main branch

## Release Process

### 1. Update Version Information

If you have version information in the code, update it first. For example, update version constants or help text.

### 2. Create and Push a Tag

Create a new tag following semantic versioning (e.g., v1.0.0, v1.1.0, v2.0.0):

```bash
# Create a new tag
git tag -a v1.0.0 -m "Release v1.0.0"

# Push the tag to GitHub
git push origin v1.0.0
```

### 3. GitHub Actions Automation

Once the tag is pushed, GitHub Actions will automatically:

1. Build binaries for multiple platforms:
   - Linux (amd64, arm64)
   - macOS (amd64, arm64)

2. Create a GitHub release with the tag

3. Upload the following artifacts to the release:
   - `imgcd-linux-amd64.tar.gz`
   - `imgcd-linux-arm64.tar.gz`
   - `imgcd-darwin-amd64.tar.gz`
   - `imgcd-darwin-arm64.tar.gz`
   - SHA256 checksums for each archive

### 4. Edit Release Notes

After the automated release is created:

1. Go to the [Releases page](https://github.com/yangliu35/imgcd/releases)
2. Click "Edit" on the newly created release
3. Add release notes describing:
   - New features
   - Bug fixes
   - Breaking changes (if any)
   - Known issues

Example release notes template:

```markdown
## What's New

- Added feature X
- Improved performance of Y
- Fixed bug Z

## Breaking Changes

- None

## Installation

Download the binary for your platform below and follow the installation instructions in the [README](https://github.com/yangliu35/imgcd#installation).

## Checksums

SHA256 checksums are provided for each binary archive. Verify your download:

\`\`\`bash
sha256sum -c imgcd-linux-amd64.tar.gz.sha256
\`\`\`
```

## Version Numbering

We follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version (v2.0.0): Incompatible API changes
- **MINOR** version (v1.1.0): New functionality in a backward compatible manner
- **PATCH** version (v1.0.1): Backward compatible bug fixes

## Troubleshooting

### Build Failed

Check the GitHub Actions logs:
1. Go to the "Actions" tab
2. Click on the failed workflow run
3. Review the logs to identify the issue

### Missing Binaries

If some binaries are missing from the release:
1. Check if all matrix builds succeeded
2. Verify the upload step didn't fail
3. If needed, delete the release and tag, fix the issue, and try again

## Manual Build (if needed)

If automated builds fail, you can build manually:

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o imgcd-linux-amd64 ./cmd/imgcd

# Linux arm64
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o imgcd-linux-arm64 ./cmd/imgcd

# macOS amd64
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o imgcd-darwin-amd64 ./cmd/imgcd

# macOS arm64
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o imgcd-darwin-arm64 ./cmd/imgcd

# Create archives
for binary in imgcd-*; do
  tar -czf ${binary}.tar.gz ${binary}
  shasum -a 256 ${binary}.tar.gz > ${binary}.tar.gz.sha256
done
```

Then manually upload these files to the GitHub release.

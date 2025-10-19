#!/bin/bash
#
# Build release binaries for all platforms
# This script mimics what GitHub Actions does for releases
#

set -e

VERSION=${1:-"dev"}
OUTPUT_DIR="dist"

echo "Building imgcd ${VERSION} for all platforms..."

# Clean output directory
rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

# Build for each platform
platforms=(
    "linux/amd64"
    "linux/arm64"
    "darwin/amd64"
    "darwin/arm64"
)

for platform in "${platforms[@]}"; do
    IFS='/' read -r -a parts <<< "$platform"
    GOOS="${parts[0]}"
    GOARCH="${parts[1]}"

    output_name="imgcd-${GOOS}-${GOARCH}"

    echo "Building ${output_name}..."

    GOOS="${GOOS}" GOARCH="${GOARCH}" CGO_ENABLED=0 \
        go build -ldflags="-s -w -X main.version=${VERSION}" \
        -o "${OUTPUT_DIR}/${output_name}" \
        ./cmd/imgcd

    # Create archive
    echo "Creating archive for ${output_name}..."
    tar -C "${OUTPUT_DIR}" -czf "${OUTPUT_DIR}/${output_name}.tar.gz" "${output_name}"

    # Generate checksum
    (cd "${OUTPUT_DIR}" && shasum -a 256 "${output_name}.tar.gz" > "${output_name}.tar.gz.sha256")

    # Remove the binary (keep only the archive)
    rm "${OUTPUT_DIR}/${output_name}"

    echo "✓ ${output_name}.tar.gz created"
done

echo ""
echo "All builds completed successfully!"
echo "Artifacts are in: ${OUTPUT_DIR}/"
echo ""
ls -lh "${OUTPUT_DIR}/"

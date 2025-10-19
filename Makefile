.PHONY: build install clean test fmt vet check all release release-test

# Binary name
BINARY=imgcd
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build the binary
build:
	go build -o $(BINARY) ./cmd/imgcd

# Install the binary to /usr/local/bin
install: build
	sudo mv $(BINARY) /usr/local/bin/

# Clean build artifacts
clean:
	rm -f $(BINARY)
	rm -rf out/
	rm -rf dist/

# Run tests
test:
	go test -v ./...

# Format code
fmt:
	go fmt ./...

# Run go vet
vet:
	go vet ./...

# Run all checks
check: fmt vet test

# Build release binaries for all platforms
release:
	@./scripts/build-release.sh $(VERSION)

# Test release build locally
release-test:
	@./scripts/build-release.sh test

# Default target
all: check build

BINARY_NAME := solana-validator-snapshot-keeper
VERSION := $(shell cat cmd/version.txt 2>/dev/null | tr -d '\n' || echo "dev")
BUILD_DIR := bin
MAIN_PKG := ./cmd/solana-validator-snapshot-keeper
MOCK_PKG := ./mock-server
LDFLAGS := -ldflags="-s -w"

# Platforms for release builds (used by build-all)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build build-mock build-all test test-cover clean dev dev-mock-server fmt vet tidy

build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PKG)

build-mock:
	cd mock-server && go build -o ../$(BUILD_DIR)/mock-server .

# Cross-platform release builds: versioned binaries, gzipped, with sha256 (for GitHub Release)
build-all:
	@echo "Building $(BINARY_NAME) for all platforms..."
	@mkdir -p $(BUILD_DIR)
	@for platform in $(PLATFORMS); do \
		OS=$$(echo $$platform | cut -d'/' -f1); \
		ARCH=$$(echo $$platform | cut -d'/' -f2); \
		OUTPUT_NAME=$(BINARY_NAME)-$(VERSION)-$$OS-$$ARCH; \
		echo "Building for $$OS/$$ARCH..."; \
		CGO_ENABLED=0 GOOS=$$OS GOARCH=$$ARCH go build $(LDFLAGS) -o $(BUILD_DIR)/$$OUTPUT_NAME $(MAIN_PKG); \
	done
	@echo "Compressing binaries..."
	@cd $(BUILD_DIR) && \
	for binary in $(BINARY_NAME)-$(VERSION)-*; do \
		if [ -f "$$binary" ]; then \
			echo "Compressing $$binary..."; \
			gzip -f "$$binary"; \
		fi; \
	done
	@echo "Generating checksums..."
	@cd $(BUILD_DIR) && \
	for binary in $(BINARY_NAME)-$(VERSION)-*.gz; do \
		if [ -f "$$binary" ]; then \
			sha256sum "$$binary" > "$$binary.sha256"; \
		fi; \
	done
	@echo "Build complete. Artifacts in $(BUILD_DIR)/"

test:
	go test -v -race ./...

test-cover:
	go test -race -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -html=coverage.txt -o coverage.html

clean:
	rm -rf $(BUILD_DIR) coverage.txt coverage.html /tmp/solana-snapshot-keeper-dev

dev-mock-server: build-mock
	@mkdir -p /tmp/solana-snapshot-keeper-dev
	$(BUILD_DIR)/mock-server -config mock-server/config.yaml

dev: build
	@mkdir -p /tmp/solana-snapshot-keeper-dev
	$(BUILD_DIR)/$(BINARY_NAME) run -c config.dev.yml

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy
	cd mock-server && go mod tidy

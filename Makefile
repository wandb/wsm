GO_VERSION = 1.24.0

# Use a specific version of golangci-lint that is built with Go 1.24
GOLANGCI_LINT_VERSION = 1.57.0

# Install system dependencies required for building
install-deps:
	@echo "Installing system dependencies..."
ifeq ($(shell uname -s),Darwin)
	@echo "Installing dependencies for macOS..."
	@brew install gpgme
else ifeq ($(shell uname -s),Linux)
	@echo "Installing dependencies for Linux..."
	@sudo apt-get update && sudo apt-get install -y libgpgme-dev libassuan-dev
else
	@echo "Please install GPGME development libraries manually for your system"
endif

install-lint:
	@echo "Installing golangci-lint version $(GOLANGCI_LINT_VERSION)..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)

# Use a simpler approach for format that doesn't rely on golangci-lint
format:
	@echo "Formatting Go files..."
	@go fmt ./...

# Add a simple lint target using Go's built-in tools
lint:
	@echo "Linting Go files..."
	@go vet ./...

# Add a build target that skips linting
build: install-deps
	@echo "Building wsm..."
ifeq ($(shell uname -s),Darwin)
	@GPGME_DIR=$$(brew --prefix gpgme) && \
	CGO_CFLAGS="-I$$GPGME_DIR/include" \
	CGO_LDFLAGS="-L$$GPGME_DIR/lib -lgpgme" \
	go build -o wsm cmd/wsm/*.go
else
	@go build -o wsm cmd/wsm/*.go
endif

# Test targets
.PHONY: test test-semver test-coverage

# Run all Go tests
test: install-deps
	@echo "Running Go tests..."
ifeq ($(shell uname -s),Darwin)
	@GPGME_DIR=$$(brew --prefix gpgme) && \
	CGO_CFLAGS="-I$$GPGME_DIR/include" \
	CGO_LDFLAGS="-L$$GPGME_DIR/lib -lgpgme" \
	go test ./... -v
else
	@go test ./... -v
endif

# Test the semver compatibility feature
test-semver: build
	@echo "Testing semver compatibility..."
	@./wsm list | grep "wandb/weave-trace" | grep -v "daily"

# Generate test coverage report
test-coverage: install-deps
	@echo "Generating test coverage report..."
ifeq ($(shell uname -s),Darwin)
	@GPGME_DIR=$$(brew --prefix gpgme) && \
	CGO_CFLAGS="-I$$GPGME_DIR/include" \
	CGO_LDFLAGS="-L$$GPGME_DIR/lib -lgpgme" \
	go test ./... -v -coverprofile=coverage.out
else
	@go test ./... -v -coverprofile=coverage.out
endif
	@go tool cover -html=coverage.out -o coverage.html

# Add an all target that runs lint, build, and test
all: format lint build test

.PHONY: install-deps install-lint format lint build all test test-semver test-coverage

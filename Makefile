GO_VERSION = 1.24.0

# Use a specific version of golangci-lint that is built with Go 1.24
GOLANGCI_LINT_VERSION = 1.57.0

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
build:
	@echo "Building wsm..."
	@go build -o wsm cmd/wsm/*.go

# Test targets
.PHONY: test test-semver test-coverage

# Run all Go tests
test:
	@echo "Running Go tests..."
	@go test ./... -v

# Test the semver compatibility feature
test-semver: build
	@echo "Testing semver compatibility..."
	@./wsm list | grep "wandb/weave-trace" | grep -v "daily"

# Generate test coverage report
test-coverage:
	@echo "Generating test coverage report..."
	@go test ./... -v -coverprofile=coverage.out
	@go tool cover -html=coverage.out -o coverage.html

# Add an all target that runs lint, build, and test
all: format lint build test

.PHONY: install-lint format lint build all

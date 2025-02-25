GO_VERSION = 1.23.0

GOLANGCI_LINT_VERSION = v1.64.5

# Set environment variables to suppress linker warnings on macOS
ifeq ($(shell uname),Darwin)
	export CGO_LDFLAGS=-Wl,-w
	export LDFLAGS=-w
endif

build:
	go build -o wsm ./cmd/wsm

# Modern linter installation
install-lint:
	@if ! [ -x "$$(command -v golangci-lint)" ]; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		GO111MODULE=on GOFLAGS="-buildvcs=false" go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	else \
		CURRENT_VERSION=$$(golangci-lint --version | grep -o 'v[0-9]\+\.[0-9]\+\.[0-9]\+' | head -1); \
		if [ "$$CURRENT_VERSION" != "$(GOLANGCI_LINT_VERSION)" ]; then \
			echo "Updating golangci-lint from $$CURRENT_VERSION to $(GOLANGCI_LINT_VERSION)..."; \
			GO111MODULE=on GOFLAGS="-buildvcs=false" go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
		fi \
	fi	

clean-lint:
	@echo "Removing golangci-lint..."
	@rm -f $(shell which golangci-lint 2>/dev/null) || true
	@echo "golangci-lint removed"

lint: install-lint
	@echo "Linting Go files..."
	@go vet ./...
	@GOGC=off golangci-lint run

lint-fix: install-lint
	@echo "Fixing lint errors..."
	@GOGC=off golangci-lint run --fix ./...

fmt:
	go fmt ./...

test:
	go test -v -cover ./...

# Latest minor of patch version of all dependencies
safe-update-deps:
	go get -u ./...
	go mod tidy

.PHONY: install-lint lint lint-fix fmt test build clean-lint safe-update-deps

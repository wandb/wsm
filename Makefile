GO_VERSION = 1.25.0

GOLANGCI_LINT_VERSION = v2.11.4

# Set environment variables to suppress linker warnings on macOS
ifeq ($(shell uname),Darwin)
	export CGO_LDFLAGS=-Wl,-w
	export LDFLAGS=-w
endif

ifeq ($(OS),Windows_NT)
	INSTALL_DIR ?= $(USERPROFILE)/bin
	BINARY = wsm.exe
else
	INSTALL_DIR ?= /usr/local/bin
	BINARY = wsm
endif

build:
	CGO_ENABLED=0 go build -tags containers_image_openpgp -o $(BINARY) ./cmd/wsm

install: build
	install -d $(INSTALL_DIR)
	install $(BINARY) $(INSTALL_DIR)/$(BINARY)

# Modern linter installation
install-lint:
	@if ! [ -x "$$(command -v golangci-lint)" ]; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	else \
		CURRENT_VERSION=$$(golangci-lint version | head -n 1 | awk '{print $$4}'); \
		TARGET_VERSION=$$(echo $(GOLANGCI_LINT_VERSION) | sed 's/^v//'); \
		if [ "$$CURRENT_VERSION" != "$$TARGET_VERSION" ]; then \
			echo "Updating golangci-lint from $$CURRENT_VERSION to $(GOLANGCI_LINT_VERSION)..."; \
			curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
		else \
			echo "golangci-lint $(GOLANGCI_LINT_VERSION) is already installed"; \
		fi \
	fi

# Clean up golangci-lint if you have the brew version which is compiled with go 1.23.x
clean-lint:
	@echo "Removing golangci-lint..."
	@rm -f $(shell which golangci-lint 2>/dev/null) || true
	@echo "golangci-lint removed"

lint: install-lint
	@echo "Linting Go files..."
	@go vet ./...
	@echo "Running golangci-lint..."
	@GOGC=off golangci-lint run --timeout=5m --concurrency=4 --max-same-issues=20

lint-fix: install-lint
	@echo "Fixing lint errors..."
	@GOGC=off golangci-lint run --fix --timeout=5m --concurrency=4 --max-same-issues=20 -v ./...

fmt:
	go fmt ./...

# Latest minor of patch version of all dependencies
safe-update-deps:
	go get -u ./...
	go mod tidy

.PHONY: install-lint lint lint-fix fmt test build install clean-lint safe-update-deps

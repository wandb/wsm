GO_VERSION = 1.22.0

GOLANGCI_LINT_VERSION = 1.58.1

install-lint:
	@if ! [ -x "$$(command -v golangci-lint)" ]; then \
		echo "Installing golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	else \
		echo "golangci-lint is already installed"; \
	fi

format: install-lint
	golangci-lint run

.PHONY: install-lint format

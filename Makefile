SHELL := /bin/bash

PROVIDER_NAME := indigo
BINARY_NAME := terraform-provider-indigo
VERSION ?= 0.1.0
OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)

PLUGIN_BASE ?= $(HOME)/.terraform.d/plugins/registry.terraform.io/local/$(PROVIDER_NAME)/$(VERSION)/$(OS)_$(ARCH)
PLUGIN_BIN := $(PLUGIN_BASE)/$(BINARY_NAME)_v$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help
help:  ## Display this help documents
	@grep -E '^[0-9a-zA-Z_-]+:.*?## .*$$' ${MAKEFILE_LIST} | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-40s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Download dependencies
	go mod download

.PHONY: build
build: deps ## Build the binary
	go build -o bin/$(BINARY_NAME) .

.PHONY: install
install: deps ## Install the plugin
	@mkdir -p $(PLUGIN_BASE)
	go build -o $(PLUGIN_BIN) .
	@echo "Installed: $(PLUGIN_BIN)"
	@echo
	@echo "Use in Terraform:"
	@echo 'terraform {'
	@echo '  required_providers {'
	@echo '    indigo = {'
	@echo '      source  = "local/indigo"'
	@echo '      version = "$(VERSION)"'
	@echo '    }'
	@echo '  }'
	@echo '}'
	@echo
	@echo "For terraform init compatibility, add this to ~/.terraformrc:"
	@echo 'provider_installation {'
	@echo '  filesystem_mirror {'
	@echo '    path    = "$(HOME)/.terraform.d/plugins"'
	@echo '    include = ["local/indigo"]'
	@echo '  }'
	@echo '  direct {'
	@echo '    exclude = ["local/indigo"]'
	@echo '  }'
	@echo '}'

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: test-client
test-client: ## Run client tests
	go test ./internal/client

.PHONY: clean
clean: ## Clean the binary and plugin
	rm -rf bin/

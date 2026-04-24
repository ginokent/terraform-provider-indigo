SHELL := /bin/bash

PROVIDER_NAME := indigo
BINARY_NAME := terraform-provider-indigo
VERSION ?= 0.1.0
OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)

PLUGIN_BASE ?= $(HOME)/.terraform.d/plugins/registry.terraform.io/local/$(PROVIDER_NAME)/$(VERSION)/$(OS)_$(ARCH)
PLUGIN_BIN := $(PLUGIN_BASE)/$(BINARY_NAME)_v$(VERSION)

.PHONY: deps build install test test-client clean

deps:
	go mod download

build: deps
	go build -o bin/$(BINARY_NAME) ./main

install: deps
	@mkdir -p $(PLUGIN_BASE)
	go build -o $(PLUGIN_BIN) ./main
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

test:
	go test ./...

test-client:
	go test ./internal/client

clean:
	rm -rf bin/

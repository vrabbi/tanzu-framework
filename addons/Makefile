# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

include ../common.mk

.DEFAULT_GOAL := help

ROOT_DIR	:= $(dir $(shell pwd))

# Active module mode, as we use go modules to manage dependencies
export GO111MODULE := on

# Image URL to use all building/pushing image targets
IMG ?= addons-controller:$(shell git describe --always --dirty --tags)

# Directories
BIN_DIR       := bin
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(TOOLS_DIR)/bin
KUBEBUILDER_BIN_DIR := $(TOOLS_BIN_DIR)/kubebuilder/bin

export PATH := $(abspath $(BIN_DIR)):$(PATH)

# Binaries
MANAGER       := $(BIN_DIR)/manager

.PHONY: all
all: test manager ## Tests and builds the binaries

## --------------------------------------
## Help
## --------------------------------------

help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

## --------------------------------------
## Testing
## --------------------------------------

.PHONY: test
test: fmt vet ## Run Tests
	$(MAKE) kubebuilder -C ${ROOT_DIR}$(TOOLS_DIR)
	KUBEBUILDER_ASSETS=${ROOT_DIR}$(KUBEBUILDER_BIN_DIR) go test ./... -coverprofile cover.out -v 2
	$(MAKE) test -C pinniped/post-deploy

.PHONY: test-verbose
test-verbose: ## Verbose tests with streaming output for debugging
	ginkgo -v ./...

## --------------------------------------
## Binaries
## --------------------------------------

.PHONY: manager-binary
manager-binary: $(MANAGER) ## Build manager binary
$(MANAGER):
	go build -o $@ -ldflags "$(LD_FLAGS) "'-extldflags "-static"'

.PHONY: manager
manager: fmt vet manager-binary ## Build manager binary

## --------------------------------------
## Linting and fixing linter errors
## --------------------------------------

.PHONY: fmt
fmt: ## Fmt codebase
	go fmt ./...

.PHONY: vet
vet: ## Vet codebase
	go vet ./...

## --------------------------------------
## Development - local
## --------------------------------------

.PHONY: run
run: fmt vet ## Run locally
	go run -ldflags "$(LD_FLAGS)" ./main.go

## --------------------------------------
## Docker
## --------------------------------------

.PHONY: docker-build
docker-build: ## Build the docker image
	cd .. && docker build -t $(IMG) -f addons/Dockerfile --build-arg LD_FLAGS="$(LD_FLAGS)" .

.PHONY: docker-push
docker-push: ## Push the docker image
	docker push $(IMG)

.PHONY: docker-remove
docker-remove: ## Remove the docker image
	docker rmi $(IMG) 2>/dev/null || true

## --------------------------------------
## Clean
## --------------------------------------

.PHONY: clean ## Remove all generated files
clean: docker-remove
	rm -rf bin

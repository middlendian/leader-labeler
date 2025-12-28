# Application name
APP_NAME := leader-labeler
REGISTRY ?= ghcr.io/middlendian
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOTEST := $(GOCMD) test
GOMOD := $(GOCMD) mod
GOFMT := $(GOCMD) fmt

# Build directory
BUILD_DIR := bin

.PHONY: help
help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary
	@echo "Building $(APP_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(APP_NAME) -ldflags "-X main.version=$(VERSION)" .
	@echo "Binary created at $(BUILD_DIR)/$(APP_NAME)"

.PHONY: test
test: ## Run tests
	@echo "Running tests..."
	$(GOTEST) -v -race -cover ./...

.PHONY: clean
clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@$(GOCMD) clean -cache -testcache
	@echo "Clean complete"

.PHONY: fmt
fmt: ## Format Go code
	@echo "Formatting code..."
	$(GOFMT) ./...

.PHONY: lint
lint: ## Run linter (requires golangci-lint)
	@echo "Running linter..."
	@golangci-lint run

.PHONY: image
image: ## Build Docker image for local architecture
	@echo "Building Docker image..."
	docker build -t $(REGISTRY)/$(APP_NAME):$(VERSION) .

.PHONY: check
check: fmt test lint image ## Run all checks and validations
	@echo "Verification complete"

.DEFAULT_GOAL := help

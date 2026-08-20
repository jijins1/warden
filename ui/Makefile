# Warden UI Makefile

.PHONY: help install build build:dev build:prod serve test lint docker docker:push clean

# Colors
GREEN := \033[0;32m
YELLOW := \033[1;33m
RED := \033[0;31m
NC := \033[0m

# Project name
PROJECT := warden-ui

# Docker
DOCKER_REGISTRY ?= ghcr.io/jijins1
DOCKER_IMAGE := $(DOCKER_REGISTRY)/$(PROJECT)
DOCKER_TAG ?= latest

help: ## Show this help message
	@echo "Usage: make <target>"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[0;32m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""

install: ## Install dependencies
	@echo "$(YELLOW)Installing dependencies...$(NC)"
	npm install
	@echo "$(GREEN)Dependencies installed!$(NC)"

build: build:prod ## Build for production

build:dev: ## Build for development
	@echo "$(YELLOW)Building for development...$(NC)"
	npm run build:dev
	@echo "$(GREEN)Development build complete!$(NC)"

build:prod: ## Build for production
	@echo "$(YELLOW)Building for production...$(NC)"
	npm run build
	@echo "$(GREEN)Production build complete!$(NC)"

serve: ## Start development server
	@echo "$(YELLOW)Starting development server...$(NC)"
	npm run start

lint: ## Run linting
	@echo "$(YELLOW)Running linter...$(NC)"
	npm run lint

test: ## Run tests
	@echo "$(YELLOW)Running tests...$(NC)"
	npm run test

# Docker targets
docker: ## Build Docker image
	@echo "$(YELLOW)Building Docker image...$(NC)"
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .
	@echo "$(GREEN)Docker image built: $(DOCKER_IMAGE):$(DOCKER_TAG)$(NC)"

docker:push: ## Build and push Docker image
	@echo "$(YELLOW)Building and pushing Docker image...$(NC)"
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	@echo "$(GREEN)Docker image pushed: $(DOCKER_IMAGE):$(DOCKER_TAG)$(NC)"

# Clean targets
clean: ## Clean build artifacts
	@echo "$(YELLOW)Cleaning build artifacts...$(NC)"
	rm -rf dist/ out-tsc/ node_modules/
	@echo "$(GREEN)Clean complete!$(NC)"

clean:hard: ## Clean everything including Docker
	make clean
	docker rmi -f $(DOCKER_IMAGE):$(DOCKER_TAG) 2>/dev/null || true
	@echo "$(GREEN)Hard clean complete!$(NC)"

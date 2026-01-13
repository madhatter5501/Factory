# Factory - AI Development Pipeline Orchestrator
# Makefile for building and running the factory

VERSION ?= dev
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -X 'main.version=$(VERSION)' \
           -X 'main.gitCommit=$(GIT_COMMIT)' \
           -X 'main.buildTime=$(BUILD_TIME)'

.PHONY: all build clean test run init status dry-run deps fmt lint help

all: build

## Build the factory binary
build:
	@echo "Building factory..."
	go build -ldflags "$(LDFLAGS)" -o bin/factory ./cmd/factory
	@echo "Built bin/factory"

## Clean build artifacts
clean:
	rm -rf bin/
	rm -rf .worktrees/

## Run tests
test:
	go test -v ./...

## Run the factory orchestrator
run: build
	./bin/factory --repo=../..

## Initialize a new kanban board
init: build
	./bin/factory --repo=../.. --init

## Show board status
status: build
	./bin/factory --repo=../.. --status

## Run in dry-run mode (no agents actually spawned)
dry-run: build
	./bin/factory --repo=../.. --dry-run --verbose

## Run with custom settings
run-custom: build
	./bin/factory --repo=../.. \
		--max-agents=$(MAX_AGENTS) \
		--timeout=$(TIMEOUT) \
		--interval=$(INTERVAL) \
		$(if $(AUTO_MERGE),--auto-merge,)

## Install dependencies
deps:
	go mod tidy

## Format code
fmt:
	gofmt -w .

## Lint code
lint:
	@if command -v golangci-lint &> /dev/null; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

## Show help
help:
	@echo "Factory - AI Development Pipeline Orchestrator"
	@echo ""
	@echo "Usage:"
	@echo "  make build     - Build the factory binary"
	@echo "  make run       - Run the orchestrator"
	@echo "  make init      - Initialize a new kanban board"
	@echo "  make status    - Show board status"
	@echo "  make dry-run   - Run without spawning agents"
	@echo "  make test      - Run tests"
	@echo "  make clean     - Clean build artifacts"
	@echo ""
	@echo "Environment variables:"
	@echo "  VERSION      - Build version (default: dev)"
	@echo "  MAX_AGENTS   - Max parallel agents (default: 3)"
	@echo "  TIMEOUT      - Agent timeout (default: 30m)"
	@echo "  INTERVAL     - Cycle interval (default: 10s)"
	@echo "  AUTO_MERGE   - Auto-merge completed tickets (default: false)"

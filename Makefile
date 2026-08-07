BINARY  := dark-canary
BIN_DIR := bin
PKG     := ./cmd/dark-canary

.PHONY: all build test test-lua tidy clean help lint setup

.DEFAULT_GOAL := help

## help: show this help
help:
	@awk '/^## [a-zA-Z0-9_-]+:/ { l=$$0; sub(/^## /,"",l); i=index(l,":"); \
	         printf "  %-14s %s\n", substr(l,1,i-1), substr(l,i+2); next } \
	     /^[a-zA-Z0-9_-]+:.*## / { i=index($$0,":"); j=index($$0,"## "); \
	         printf "  %-14s %s\n", substr($$0,1,i-1), substr($$0,j+3) }' $(MAKEFILE_LIST)

all: build

## build: compile the binary into ./bin
build:
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

## test: run tests
test:
	go test -race -count=1 ./...

## test-lua: run the capture hook's tests (any Lua 5.1+, no luarocks needed)
test-lua:
	lua lua/dark_canary_test.lua

## tidy: tidy modules
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR)

setup: ## Install the pre-commit hook
	pre-commit install

lint: ## Run all pre-commit checks on the whole tree
	pre-commit run --all-files

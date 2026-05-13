# Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
# SPDX-License-Identifier: MIT

BIN_DIR := bin
BIN     := $(BIN_DIR)/wintermute
PKG     := ./...

GOFLAGS  ?=
GOTEST   ?= go test
GOBUILD  ?= go build
GOVET    ?= go vet

.PHONY: all build test test-race vet lint check-headers run clean db-reset tidy help

all: build

help:
	@echo "Targets:"
	@echo "  build         compile the binary into $(BIN)"
	@echo "  test          run unit + integration tests"
	@echo "  test-race     run tests with the race detector"
	@echo "  vet           run go vet"
	@echo "  lint          run golangci-lint (requires installation)"
	@echo "  check-headers verify every source file has the MIT SPDX header"
	@echo "  run           build then start the server with ./wintermute.toml"
	@echo "  clean         remove build artifacts"
	@echo "  db-reset      delete the local SQLite database files"
	@echo "  tidy          run go mod tidy"

build:
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) $(GOFLAGS) -o $(BIN) ./cmd/wintermute

test:
	$(GOTEST) $(GOFLAGS) $(PKG)

test-race:
	$(GOTEST) $(GOFLAGS) -race $(PKG)

vet:
	$(GOVET) $(GOFLAGS) $(PKG)

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed; see https://golangci-lint.run/"; exit 1; }
	golangci-lint run

check-headers:
	@./scripts/check-headers.sh

run: build
	$(BIN) --config wintermute.toml

clean:
	rm -rf $(BIN_DIR)

db-reset:
	@rm -f wintermute.db wintermute.db-wal wintermute.db-shm
	@echo "deleted local SQLite files"

tidy:
	go mod tidy

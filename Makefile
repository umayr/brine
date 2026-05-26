BINARY  := brine
BIN_DIR := bin
CMD     := ./cmd/brine

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION) -s -w"
BUILD_TAGS := containers_image_openpgp

.PHONY: all build clean run help

all: build

## build: compile and place binary in bin/
build:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD)
	@echo "built $(BIN_DIR)/$(BINARY) ($(VERSION))"

## run: build and run with defaults
run: build
	./$(BIN_DIR)/$(BINARY) --dev

## clean: remove the bin/ directory
clean:
	@rm -rf $(BIN_DIR)
	@echo "cleaned"

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## /  /'

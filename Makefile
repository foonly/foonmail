# foonmail build helpers.
#
# Gio's Vulkan renderer needs the vulkan-headers package at build time. The
# novulkan tag leaves it out and Gio draws with OpenGL ES instead. Override
# with `make TAGS=` once vulkan-headers is installed.
TAGS ?= novulkan
GO   ?= go
GOFLAGS_ALL = -tags=$(TAGS)

BIN      := bin
COMMANDS := foonmaild foonmail

.PHONY: all build test race vet check run run-daemon install clean help

all: build

## build: build foonmaild and foonmail into bin/
build: $(addprefix $(BIN)/,$(COMMANDS))

$(BIN)/%: FORCE
	$(GO) build $(GOFLAGS_ALL) -o $@ ./cmd/$*

## test: run all tests
test:
	$(GO) test $(GOFLAGS_ALL) ./...

## race: run all tests with the race detector
race:
	$(GO) test $(GOFLAGS_ALL) -race ./...

## vet: run go vet
vet:
	$(GO) vet $(GOFLAGS_ALL) ./...

## check: vet and race-test, as before a commit
check: vet race

## run: start the GUI
run:
	$(GO) run $(GOFLAGS_ALL) ./cmd/foonmail

## run-daemon: start the daemon in the foreground with debug logging
run-daemon:
	$(GO) run $(GOFLAGS_ALL) ./cmd/foonmaild -v

## install: install foonmaild and foonmail into $(GOBIN) or ~/go/bin
install:
	$(GO) install $(GOFLAGS_ALL) $(addprefix ./cmd/,$(COMMANDS))

## clean: remove built binaries
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST)

FORCE:

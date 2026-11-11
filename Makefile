# foonmail build helpers.
#
# Gio's Vulkan renderer needs the vulkan-headers package at build time. The
# novulkan tag leaves it out and Gio draws with OpenGL ES instead. Override
# with `make TAGS=` once vulkan-headers is installed.
TAGS ?= novulkan
GO   ?= go
GOFLAGS_ALL = -tags=$(TAGS)

# The version built in: the git tag (v1.2.3), or the tag plus commits since
# and -dirty between releases. foonver makes the tags; see README.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS  = -X foonmail/internal/version.Version=$(VERSION)

BIN      := bin
COMMANDS := foonmaild foonmail foonmailctl

.PHONY: all build test race vet check run run-daemon install uninstall clean help

all: build

## build: build foonmaild and foonmail into bin/
build: $(addprefix $(BIN)/,$(COMMANDS))

$(BIN)/%: FORCE
	$(GO) build $(GOFLAGS_ALL) -ldflags '$(LDFLAGS)' -o $@ ./cmd/$*

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

# Where `make install` puts things: per user, no root needed.
PREFIX   ?= $(HOME)/.local
BINDIR   ?= $(PREFIX)/bin
DATADIR  ?= $(PREFIX)/share
UNITDIR  ?= $(HOME)/.config/systemd/user
SYSTEMCTL ?= systemctl --user
SUBST     = sed -e 's|@BINDIR@|$(BINDIR)|g'

DESKTOP_FILE := $(DATADIR)/applications/foonmail.desktop
DBUS_FILES   := $(DATADIR)/dbus-1/services/io.github.foonly.Foonmail.service \
                $(DATADIR)/dbus-1/services/io.github.foonly.Foonmail.GUI.service
UNIT_FILE    := $(UNITDIR)/foonmaild.service

## install: install the programs to ~/.local/bin, with a desktop file, D-Bus
##          activation and the foonmaild systemd unit (enabled and restarted)
install: build
	install -d $(BINDIR) $(DATADIR)/applications $(DATADIR)/dbus-1/services $(UNITDIR)
	@# A running binary can't be overwritten ("text file busy"), so copy then rename.
	for c in $(COMMANDS); do install -m755 $(BIN)/$$c $(BINDIR)/.$$c.new && mv -f $(BINDIR)/.$$c.new $(BINDIR)/$$c; done
	$(SUBST) contrib/foonmail.desktop.in > $(DESKTOP_FILE)
	$(SUBST) contrib/dbus/io.github.foonly.Foonmail.service.in > $(DATADIR)/dbus-1/services/io.github.foonly.Foonmail.service
	$(SUBST) contrib/dbus/io.github.foonly.Foonmail.GUI.service.in > $(DATADIR)/dbus-1/services/io.github.foonly.Foonmail.GUI.service
	$(SUBST) contrib/systemd/foonmaild.service.in > $(UNIT_FILE)
	-update-desktop-database -q $(DATADIR)/applications 2>/dev/null
	-busctl --user call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus ReloadConfig 2>/dev/null
	$(SYSTEMCTL) daemon-reload
	$(SYSTEMCTL) enable foonmaild.service
	@if $(SYSTEMCTL) restart foonmaild.service; then \
		echo "foonmaild is running under systemd: journalctl --user -u foonmaild -f"; \
	else \
		echo "foonmaild didn't start. If you run it by hand, stop that one and run: $(SYSTEMCTL) restart foonmaild"; \
	fi

## uninstall: remove what install put in place, and stop the daemon
uninstall:
	-$(SYSTEMCTL) disable --now foonmaild.service
	rm -f $(addprefix $(BINDIR)/,$(COMMANDS)) $(DESKTOP_FILE) $(DBUS_FILES) $(UNIT_FILE)
	-$(SYSTEMCTL) daemon-reload
	-update-desktop-database -q $(DATADIR)/applications 2>/dev/null
	@echo "Your config, cache and outbox are left in place."

## clean: remove built binaries
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST)

FORCE:

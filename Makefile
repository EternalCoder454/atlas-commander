BINARY  := atlas-commander
HOOK    := atlas-hook
APPID   := com.atlas.Commander
BINDIR  := bin
PREFIX  ?= $(HOME)/.local
APPDIR  := $(PREFIX)/share/applications
ICONDIR := $(PREFIX)/share/icons/hicolor/scalable/apps
SMALLICONDIR := $(PREFIX)/share/icons/hicolor/16x16/apps
SYMBOLICDIR := $(PREFIX)/share/icons/hicolor/symbolic/apps
# Where the app finds the checkout it was built from (internal/update reads it
# through paths.Data), so the in-app updater knows what to pull and rebuild.
# The record also names the installed binary, so a distro package that shares
# the prefix is not mistaken for this build, and the commit that was built, so a
# pull followed by a failed build is not reported as up to date. Writing it is
# best effort and skipped for a staged (DESTDIR) install.
DATADIR := $(or $(XDG_DATA_HOME),$(HOME)/.local/share)/atlas-commander

.PHONY: build run install uninstall clean vet test test-race

# Both programs go into bin/ together. atlas-commander registers atlas-hook as
# Claude Code's PreToolUse hook and looks for it beside its own executable, so
# they must always be installed and shipped as a pair. The first build compiles
# the MIQT C++ bindings and takes minutes; later builds come from Go's cache.
build:
	go build -trimpath -ldflags="-s -w" -o $(BINDIR)/ ./cmd/...

run: build
	./$(BINDIR)/$(BINARY)

vet:
	go vet ./...

# Sandboxed: the tests must never read or write the user's real settings or
# database. The display is unset so GUI tests skip rather than open a window.
test:
	tmp=$$(mktemp -d) && \
	env -u DISPLAY -u WAYLAND_DISPLAY \
		ATLAS_CONFIG_HOME="$$tmp/config" ATLAS_DATA_HOME="$$tmp/data" ATLAS_RUNTIME_DIR="$$tmp/run" \
		go test -count=1 ./... ; \
	status=$$? ; rm -rf "$$tmp" ; exit $$status

# The race detector, over the packages that share state between goroutines.
test-race:
	tmp=$$(mktemp -d) && \
	env -u DISPLAY -u WAYLAND_DISPLAY \
		ATLAS_CONFIG_HOME="$$tmp/config" ATLAS_DATA_HOME="$$tmp/data" ATLAS_RUNTIME_DIR="$$tmp/run" \
		go test -race -count=1 ./internal/agent/... ./internal/fleet/ ./internal/gate/ ./internal/store/ ./internal/procgroup/ ; \
	status=$$? ; rm -rf "$$tmp" ; exit $$status

install: build
	install -Dm755 $(BINDIR)/$(BINARY) $(PREFIX)/bin/$(BINARY)
	install -Dm755 $(BINDIR)/$(HOOK) $(PREFIX)/bin/$(HOOK)
	-@[ -n "$(DESTDIR)" ] || { install -d "$(DATADIR)" && { printf 'source=%s\n' "$(CURDIR)"; printf 'binary=%s\n' "$(PREFIX)/bin/$(BINARY)"; printf 'commit=%s\n' "$$(git -C "$(CURDIR)" rev-parse HEAD 2>/dev/null)"; } > "$(DATADIR)/source"; } || true
	install -Dm644 assets/icon.svg $(ICONDIR)/$(APPID).svg
	install -Dm644 assets/icon-16.svg $(SMALLICONDIR)/$(APPID).svg
	install -Dm644 assets/icon-symbolic.svg $(SYMBOLICDIR)/$(APPID)-symbolic.svg
	install -d $(APPDIR)
	sed 's|@BIN@|$(PREFIX)/bin/$(BINARY)|g' assets/$(APPID).desktop > $(APPDIR)/$(APPID).desktop
	chmod 644 $(APPDIR)/$(APPID).desktop
	-update-desktop-database $(APPDIR) 2>/dev/null || true
	-gtk-update-icon-cache -f -t $(PREFIX)/share/icons/hicolor 2>/dev/null || true
	@echo "Installed Atlas Commander — press Super and search 'Atlas'."

# Settings and the database live under ~/.config and ~/.local/share and are
# left alone: removing the program should not remove a user's audit log.
uninstall:
	rm -f $(PREFIX)/bin/$(BINARY) $(PREFIX)/bin/$(HOOK)
	rm -f $(APPDIR)/$(APPID).desktop
	rm -f "$(DATADIR)/source"
	rm -f $(ICONDIR)/$(APPID).svg $(SMALLICONDIR)/$(APPID).svg $(SYMBOLICDIR)/$(APPID)-symbolic.svg
	-update-desktop-database $(APPDIR) 2>/dev/null || true

clean:
	rm -rf $(BINDIR) dist

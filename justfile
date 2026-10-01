# The same recipes as the Makefile, for people who use just instead of make.
# Either works on its own; neither calls the other. Keep the two in step: a
# test (buildfiles_test.go) fails if their recipe names drift apart.
#
# PREFIX comes from the environment or the command line, as with make:
#   just install                 into ~/.local
#   just PREFIX=/usr/local install

binary := "atlas-commander"
hook := "atlas-hook"
appid := "com.atlas.Commander"
bindir := "bin"
PREFIX := env_var_or_default("PREFIX", home_directory() / ".local")
appdir := PREFIX / "share/applications"
icondir := PREFIX / "share/icons/hicolor/scalable/apps"
smallicondir := PREFIX / "share/icons/hicolor/16x16/apps"
symbolicdir := PREFIX / "share/icons/hicolor/symbolic/apps"
# Where the app finds the checkout it was built from, so the in-app updater
# knows what to pull and rebuild. The same place and format the Makefile writes
# (source, installed binary, built commit); skipped for a staged DESTDIR install.
destdir := env_var_or_default("DESTDIR", "")
datadir := env_var_or_default("XDG_DATA_HOME", home_directory() / ".local/share") / "atlas-commander"

# Both programs go into bin/ together. atlas-commander registers atlas-hook as
# Claude Code's PreToolUse hook and looks for it beside its own executable, so
# they must always be installed and shipped as a pair. The first build compiles
# the MIQT C++ bindings and takes minutes; later builds come from Go's cache.
build:
    go build -trimpath -ldflags="-s -w" -o {{bindir}}/ ./cmd/...

run: build
    ./{{bindir}}/{{binary}}

vet:
    go vet ./...

# Sandboxed: the tests must never read or write the user's real settings or
# database. The display is unset so GUI tests skip rather than open a window.
test:
    #!/usr/bin/env bash
    tmp=$(mktemp -d)
    env -u DISPLAY -u WAYLAND_DISPLAY \
        ATLAS_CONFIG_HOME="$tmp/config" ATLAS_DATA_HOME="$tmp/data" ATLAS_RUNTIME_DIR="$tmp/run" \
        go test -count=1 ./...
    status=$?
    rm -rf "$tmp"
    exit $status

# The race detector, over the packages that share state between goroutines.
test-race:
    #!/usr/bin/env bash
    tmp=$(mktemp -d)
    env -u DISPLAY -u WAYLAND_DISPLAY \
        ATLAS_CONFIG_HOME="$tmp/config" ATLAS_DATA_HOME="$tmp/data" ATLAS_RUNTIME_DIR="$tmp/run" \
        go test -race -count=1 ./internal/agent/... ./internal/fleet/ ./internal/gate/ ./internal/store/ ./internal/procgroup/
    status=$?
    rm -rf "$tmp"
    exit $status

install: build
    install -Dm755 {{bindir}}/{{binary}} "{{PREFIX}}/bin/{{binary}}"
    install -Dm755 {{bindir}}/{{hook}} "{{PREFIX}}/bin/{{hook}}"
    -@[ -n "{{destdir}}" ] || { install -d "{{datadir}}" && { printf 'source=%s\n' "{{justfile_directory()}}"; printf 'binary=%s\n' "{{PREFIX}}/bin/{{binary}}"; printf 'commit=%s\n' "$(git -C "{{justfile_directory()}}" rev-parse HEAD 2>/dev/null)"; } > "{{datadir}}/source"; } || true
    install -Dm644 assets/icon.svg "{{icondir}}/{{appid}}.svg"
    install -Dm644 assets/icon-16.svg "{{smallicondir}}/{{appid}}.svg"
    install -Dm644 assets/icon-symbolic.svg "{{symbolicdir}}/{{appid}}-symbolic.svg"
    install -d "{{appdir}}"
    sed 's|@BIN@|{{PREFIX}}/bin/{{binary}}|g' assets/{{appid}}.desktop > "{{appdir}}/{{appid}}.desktop"
    chmod 644 "{{appdir}}/{{appid}}.desktop"
    -update-desktop-database "{{appdir}}" 2>/dev/null || true
    -gtk-update-icon-cache -f -t "{{PREFIX}}/share/icons/hicolor" 2>/dev/null || true
    @echo "Installed Atlas Commander — press Super and search 'Atlas'."

# Settings and the database live under ~/.config and ~/.local/share and are
# left alone: removing the program should not remove a user's audit log.
uninstall:
    rm -f "{{PREFIX}}/bin/{{binary}}" "{{PREFIX}}/bin/{{hook}}"
    rm -f "{{appdir}}/{{appid}}.desktop"
    rm -f "{{datadir}}/source"
    rm -f "{{icondir}}/{{appid}}.svg" "{{smallicondir}}/{{appid}}.svg" "{{symbolicdir}}/{{appid}}-symbolic.svg"
    -update-desktop-database "{{appdir}}" 2>/dev/null || true

clean:
    rm -rf {{bindir}} dist

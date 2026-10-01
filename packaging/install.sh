#!/usr/bin/env bash
# Install Atlas Commander from a release tarball. No Go toolchain needed — the
# binaries are already built; only the Qt 6 runtime libraries have to be present.
#
#   ./install.sh            install into ~/.local
#   ./install.sh --system   install into /usr/local (needs root)
#   ./install.sh --uninstall
set -euo pipefail

APPID=com.atlas.Commander
BINARY=atlas-commander
# atlas-commander looks for the hook helper beside its own executable, so the
# two are always installed into the same folder.
HOOK=atlas-hook
PREFIX="${PREFIX:-$HOME/.local}"
action=install

for arg in "$@"; do
    case "$arg" in
        --system)    PREFIX=/usr/local ;;
        --uninstall) action=uninstall ;;
        --prefix=*)  PREFIX="${arg#--prefix=}" ;;
        -h|--help)   sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)           echo "unknown option: $arg" >&2; exit 2 ;;
    esac
done

here="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
APPDIR="$PREFIX/share/applications"
ICONDIR="$PREFIX/share/icons/hicolor/scalable/apps"
ICON16DIR="$PREFIX/share/icons/hicolor/16x16/apps"
ICONSYMDIR="$PREFIX/share/icons/hicolor/symbolic/apps"

refresh_caches() {
    update-desktop-database "$APPDIR" 2>/dev/null || true
    gtk-update-icon-cache -f -t "$PREFIX/share/icons/hicolor" 2>/dev/null || true
}

if [ "$action" = uninstall ]; then
    rm -f "$PREFIX/bin/$BINARY" "$PREFIX/bin/$HOOK" "$APPDIR/$APPID.desktop" \
        "$ICONDIR/$APPID.svg" "$ICON16DIR/$APPID.svg" "$ICONSYMDIR/$APPID-symbolic.svg"
    refresh_caches
    echo "Removed Atlas Commander from $PREFIX. Your settings and database are untouched."
    exit 0
fi

# The binary is dynamically linked against Qt 6. Ask the dynamic linker what it
# cannot resolve rather than guessing at library names, and say so now instead of
# letting the app fail at launch. If ldd is unavailable we simply skip the check —
# it is a courtesy, not a gate.
if command -v ldd >/dev/null 2>&1; then
    missing="$(ldd "$here/$BINARY" 2>/dev/null | awk '/not found/ {print $1}' | sort -u)"
    if [ -n "$missing" ]; then
        echo "This build needs libraries your system does not have:" >&2
        echo "$missing" | sed 's/^/  /' >&2
        echo >&2
        echo "On Fedora:  sudo dnf install qt6-qtbase qt6-qtbase-gui" >&2
        echo "On Debian:  sudo apt install libqt6widgets6 libqt6gui6 libqt6core6" >&2
        echo "On Arch:    sudo pacman -S qt6-base" >&2
        echo >&2
        echo "If they are installed and still listed, this tarball was built" >&2
        echo "against a newer distribution — use setup.sh instead, which builds" >&2
        echo "Atlas Commander for this system (see the README)." >&2
        exit 1
    fi
fi

install -Dm755 "$here/$BINARY"            "$PREFIX/bin/$BINARY"
install -Dm755 "$here/$HOOK"              "$PREFIX/bin/$HOOK"
install -Dm644 "$here/assets/icon.svg"    "$ICONDIR/$APPID.svg"
install -Dm644 "$here/assets/icon-16.svg" "$ICON16DIR/$APPID.svg"
install -Dm644 "$here/assets/icon-symbolic.svg" "$ICONSYMDIR/$APPID-symbolic.svg"
install -d "$APPDIR"
sed "s|@BIN@|$PREFIX/bin/$BINARY|g" "$here/assets/$APPID.desktop" > "$APPDIR/$APPID.desktop"
chmod 644 "$APPDIR/$APPID.desktop"
refresh_caches

echo "Installed Atlas Commander to $PREFIX — press Super and search 'Atlas'."

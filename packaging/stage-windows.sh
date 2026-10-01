#!/usr/bin/env bash
# Build Atlas Commander for Windows and stage everything it needs to run elsewhere.
#
#   ./stage-windows.sh [version]
#
# Run from the repository root inside an MSYS2 UCRT64 shell. Produces
# dist/atlas-commander-<version>-windows-x86_64/ and a .zip of it beside it.
#
# A Qt application on Windows needs more than its DLLs: Qt looks for a platform
# plugin (platforms/qwindows.dll) beside the exe and refuses to start without it,
# and without the image-format and icon-engine plugins every SVG icon is blank.
# This does what windeployqt does, by hand, so the set is explicit and checked.
# MSYS2's Qt is also the one thing here that is dynamically linked on purpose:
# Qt is LGPLv3, and shipping its DLLs as separate files keeps them replaceable.
#
# It lives here rather than inside a workflow because two workflows need it: the
# branch build and the release. A release that packaged differently from what CI
# tested would be the worst possible place for that difference to first appear.
set -euo pipefail

version="${1:-$(cat VERSION)}"
name="atlas-commander-${version}-windows-x86_64"
dist="dist/$name"
prefix="${MSYSTEM_PREFIX:-/ucrt64}"
tmp="${TMPDIR:-/tmp}"

# The icon and version block, compiled into an object the Go linker picks up.
#
# The name matters: Go links a .syso from the package directory into every build,
# but honours a _GOOS_GOARCH suffix — so called this, it is invisible to a Linux
# build of the same tree. It goes in the main package's directory, and is
# generated rather than committed, from the same VERSION the app reports about
# itself.
echo "==> compiling Windows resources"
syso="cmd/atlas-commander/atlas_windows_amd64.syso"
version_core="${version%%-*}"
version_comma="$(echo "$version_core" | awk -F. '{printf "%d,%d,%d,0", $1, $2, $3}')"
sed -e "s/@VERSION@/$version/g" -e "s/@V_COMMA@/$version_comma/g" \
    packaging/windows-resource.rc.in > "$tmp/atlas-resource.rc"
if command -v windres >/dev/null 2>&1; then
    # --codepage=65001 so the .rc is read as UTF-8; windres assumes CP1252
    # otherwise. The template is ASCII anyway, so this is the belt to its braces.
    windres --codepage=65001 -O coff -i "$tmp/atlas-resource.rc" -o "$syso"
    echo "    icon and version block: $(stat -c%s "$syso") bytes"
else
    echo "    windres not found; building without an icon or version block" >&2
fi

rm -rf "$dist"
mkdir -p "$dist"

echo "==> building atlas-commander.exe"
# -H=windowsgui detaches it from a console, so launching it does not leave a black
# window behind — which is what a command-line program looks like.
CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -H=windowsgui" -o "$dist/atlas-commander.exe" ./cmd/atlas-commander

# ...and the same program with its console left attached.
#
# The cost of windowsgui is that nothing the program writes to stderr goes
# anywhere. If Qt cannot find a plugin, it says so on stderr and the user sees an
# application that does not start and gives no reason, so the diagnosable build
# ships beside the normal one. It is linked from the same sources, so the staging
# that works for one works for both.
echo "==> building the console build"
CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o "$dist/atlas-commander-console.exe" ./cmd/atlas-commander
rm -f "$syso"

# The hook helper is pure Go with no Qt, so it has no DLLs to collect. It has to
# sit in the same folder: Commander finds it beside its own executable.
echo "==> building atlas-hook.exe"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$dist/atlas-hook.exe" ./cmd/atlas-hook

echo "==> collecting DLLs"
# Taken from what the linker actually recorded rather than a hand-kept list. ldd
# names the ucrt64 ones by absolute path; the system ones under /c/WINDOWS are
# already on the machine and must not be shipped.
list="$tmp/atlas-dll-all.txt"
ldd "$dist/atlas-commander.exe" | awk '/=> \/ucrt64/ {print $3}' | sort -u > "$list"
echo "    directly linked: $(wc -l < "$list")"

# Qt's plugins are loaded at run time, so ldd on the exe never sees them. Copy the
# ones the app needs into the layout Qt searches: <plugin kind>/<plugin>.dll beside
# the exe. Anything not listed here is not shipped, which keeps the zip small.
plugindir=""
for d in "$prefix/share/qt6/plugins" "$prefix/lib/qt6/plugins"; do
    [ -d "$d/platforms" ] && { plugindir="$d"; break; }
done
[ -n "$plugindir" ] || { echo "no Qt plugin directory found under $prefix" >&2; exit 1; }
for plugin in platforms/qwindows.dll platforms/qoffscreen.dll \
              styles/qmodernwindowsstyle.dll \
              imageformats/qsvg.dll imageformats/qico.dll imageformats/qjpeg.dll imageformats/qgif.dll \
              iconengines/qsvgicon.dll \
              tls/qschannelbackend.dll tls/qcertonlybackend.dll; do
    if [ -f "$plugindir/$plugin" ]; then
        mkdir -p "$dist/$(dirname "$plugin")"
        cp "$plugindir/$plugin" "$dist/$plugin"
        echo "$dist/$plugin" >> "$list"
    fi
done

# ...and what those need in turn. Qt pulls in a deep tree — ICU, HarfBuzz,
# FreeType, PCRE2, the lot — so walk it to a fixed point rather than guessing a
# depth. Six rounds is a bound, not an expectation; it settles in three or four.
# Plugins are walked too, which is how the SVG plugin's libQt6Svg gets in.
for round in 1 2 3 4 5 6; do
    before=$(sort -u "$list" | wc -l)
    while read -r dll; do
        ldd "$dll" 2>/dev/null | awk '/=> \/ucrt64/ {print $3}'
    done < "$list" | sort -u > "$tmp/atlas-dll-next.txt"
    sort -u "$list" "$tmp/atlas-dll-next.txt" > "$tmp/atlas-dll-merged.txt"
    mv "$tmp/atlas-dll-merged.txt" "$list"
    after=$(wc -l < "$list")
    echo "    round $round: $before -> $after"
    [ "$before" != "$after" ] || break
done
# Only the DLLs from the prefix go beside the exe; the plugins are already placed.
grep -v "^$dist/" "$list" | xargs -r cp -t "$dist"
echo "    shipping $(grep -vc "^$dist/" "$list") DLLs"

# Check the ones it cannot run without are actually there.
#
# If ldd fails to resolve anything — it is not guaranteed to work on every PE file,
# and a version that printed nothing would leave the list empty — then cp copies
# nothing, the zip builds, and the result is an exe that dies on launch with a
# missing-DLL box. Naming the libraries rather than counting them says what is wrong
# when it is wrong.
for must in Qt6Core Qt6Gui Qt6Widgets; do
    ls "$dist"/${must}*.dll >/dev/null 2>&1 \
        || { echo "no $must DLL was collected; the build would not run" >&2; exit 1; }
done
test -f "$dist/platforms/qwindows.dll" \
    || { echo "qwindows.dll is missing; Qt would refuse to start" >&2; exit 1; }
test -f "$dist/imageformats/qsvg.dll" \
    || { echo "the SVG image plugin is missing; every icon would be blank" >&2; exit 1; }
echo "    Qt Core, Gui, Widgets, the Windows platform plugin and SVG all present"

cp README.md LICENSE NOTICE CHANGELOG.md "$dist/"

# A note in the folder, for whoever opens it after the app has not started.
cat > "$dist/TROUBLESHOOTING.txt" <<'TXT'
Atlas Commander - if it does not start
======================================

atlas-commander.exe runs without a console, so anything it complains about on the
way up is discarded. atlas-commander-console.exe in this folder is the same
program with the console attached.

Open a terminal in this folder and run:

    atlas-commander-console.exe

Whatever Qt or Atlas has to say will appear there.

The usual cause is a file missing from this folder. Atlas needs the DLLs and the
platforms\, styles\, imageformats\ and iconengines\ folders sitting beside the
exe, and atlas-hook.exe beside it too (tool approvals do not work without it), so
run it where you unpacked it rather than copying the exe somewhere on its own.

If the window comes up blank or the program crashes at start on a graphics
driver, try software rendering:

    set QT_QUICK_BACKEND=software
    set QT_OPENGL=software
    atlas-commander-console.exe

Please include the console output in a bug report:
https://github.com/EternalCoder454/atlas-commander/issues
TXT

echo "==> zipping"
( cd dist && zip -qr "$name.zip" "$name" && sha256sum "$name.zip" > "$name.zip.sha256" )
ls -l "dist/$name.zip"
echo "==> done: dist/$name.zip"

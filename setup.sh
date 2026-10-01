#!/usr/bin/env bash
# Atlas Commander: install, update or remove it on any Linux distribution.
#
#   curl -fsSL https://raw.githubusercontent.com/EternalCoder454/atlas-commander/release/setup.sh | bash
#
# or, from a copy of this file or a checkout:
#
#   bash setup.sh                 install (the default)
#   bash setup.sh update          update an installed copy
#   bash setup.sh uninstall       remove it; add --purge to remove settings and data too
#   bash setup.sh check           say what this machine has and what it needs
#
# Options:
#   --beta          follow the beta branch instead of release
#   --yes           do not ask before installing packages
#   --purge         with uninstall: also remove settings, the database and the audit log
#
# Commander is built from source with your distribution's own Qt 6 (6.5 or
# newer), so the app follows your desktop's theme and fonts. Nothing here writes
# outside your home directory except through your package manager, which asks for
# your password itself.
set -euo pipefail

REPO="https://github.com/EternalCoder454/atlas-commander"
RAW="https://raw.githubusercontent.com/EternalCoder454/atlas-commander"
PREFIX="$HOME/.local"
# Where this script keeps its own files: the checkout it builds from. Not
# share/atlas-commander: that is where the app keeps its database and audit log
# (internal/paths), and removing the checkout must never take them with it.
DATA="$PREFIX/share/atlas-commander-setup"
SRC="$DATA/src"
NEED_QT="6.5"
NEED_GO="1.27"

action="install"
channel=""
yes=0
purge=0

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
note() { printf '  %s\n' "$*"; }
die()  { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,18p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; }

for arg in "$@"; do
	case "$arg" in
	install | update | uninstall | check) action="$arg" ;;
	--beta) channel="beta" ;;
	--release) channel="release" ;;
	--yes | -y) yes=1 ;;
	--purge) purge=1 ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option '$arg' (try --help)" ;;
	esac
done

# ask QUESTION: yes unless the answer is no. Piped from curl, stdin is this
# script, so the answer is read from the terminal instead.
ask() {
	[ "$yes" = 1 ] && return 0
	local reply=""
	# /dev/tty exists without a terminal behind it (ssh without -t, cron);
	# opening it is the test that tells.
	if { : </dev/tty; } 2>/dev/null; then
		printf '%s [Y/n] ' "$1" >/dev/tty
		# No answer at all — the terminal closed — is not a yes.
		read -r reply </dev/tty || return 1
	else
		die "$1 — rerun with --yes to answer yes without a terminal"
	fi
	case "$reply" in [nN]*) return 1 ;; *) return 0 ;; esac
}

# as_root runs a command with the privileges packages need.
as_root() {
	if [ "$(id -u)" = 0 ]; then
		"$@"
	elif command -v sudo >/dev/null; then
		sudo "$@"
	elif command -v doas >/dev/null; then
		doas "$@"
	else
		die "installing packages needs root, and neither sudo nor doas is here: run '$*' as root, then this again"
	fi
}

have() { command -v "$1" >/dev/null 2>&1; }

# version_ge A B: whether version A is at least B.
version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

distro_name() {
	if [ -r /etc/os-release ]; then
		# shellcheck disable=SC1091
		(. /etc/os-release && printf '%s' "${PRETTY_NAME:-${NAME:-Linux}}")
	else
		printf 'this Linux'
	fi
}

# --- Packages -----------------------------------------------------------------

# The package manager, and what it calls everything a build needs.
pm="" packages="" install_cmd=()
detect_pm() {
	if have pacman; then
		pm="pacman"; packages="go qt6-base base-devel git pkgconf"
		install_cmd=(pacman -S --needed --noconfirm)
	elif have apt-get; then
		pm="apt"; packages="golang-go qt6-base-dev build-essential pkg-config git"
		install_cmd=(apt-get install -y)
	elif have dnf; then
		pm="dnf"; packages="golang qt6-qtbase-devel gcc-c++ pkgconf-pkg-config git make"
		install_cmd=(dnf install -y)
	elif have zypper; then
		pm="zypper"; packages="go qt6-base-devel gcc-c++ pkg-config git make"
		install_cmd=(zypper --non-interactive install)
	elif have xbps-install; then
		pm="xbps"; packages="go qt6-base-devel gcc pkg-config git make"
		install_cmd=(xbps-install -Sy)
	elif have apk; then
		pm="apk"; packages="go qt6-qtbase-dev build-base pkgconf git"
		install_cmd=(apk add)
	else
		pm=""
	fi
}

# What a build is missing, one short reason per line; empty when nothing.
missing_native() {
	have git || echo "git"
	have make || echo "make"
	{ have g++ || have c++ || have clang++; } || echo "a C++ compiler"
	{ have pkg-config || have pkgconf; } || echo "pkg-config"
	if have go; then
		local v
		v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
		# Go 1.21 and later fetch the toolchain go.mod asks for by themselves.
		version_ge "${v:-0}" "1.21" || echo "Go $NEED_GO or newer (this one is $v)"
	else
		echo "Go"
	fi
	if have pkg-config || have pkgconf; then
		pkg-config --exists Qt6Widgets || echo "the Qt 6 development files"
	fi
}

# Why the system's Qt cannot build Commander, or nothing when it can.
too_old() {
	local qt
	qt="$(pkg-config --modversion Qt6Widgets 2>/dev/null || true)"
	[ -n "$qt" ] && ! version_ge "$qt" "$NEED_QT" && echo "Qt $qt (Commander needs $NEED_QT)"
	return 0
}

install_packages() {
	[ -n "$pm" ] || die "no package manager this script knows. Install Go $NEED_GO+, a C++ compiler, pkg-config, git, make and the Qt $NEED_QT+ development files, then run this again."
	say "Installing what the build needs with $pm:"
	note "${install_cmd[*]} $packages"
	ask "Go ahead?" || die "nothing installed"
	if [ "$pm" = "apt" ]; then
		as_root apt-get update
	fi
	# Word splitting is the point: $packages is a list.
	# shellcheck disable=SC2086
	as_root "${install_cmd[@]}" $packages
}

# --- Install --------------------------------------------------------------------

installed_channel() {
	local b=""
	[ -d "$SRC/.git" ] && b="$(git -C "$SRC" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
	# A detached checkout has no branch to follow; release is the default.
	if [ -z "$b" ] || [ "$b" = HEAD ]; then b="release"; fi
	printf '%s' "$b"
}

fetch_source() {
	local want="$1"
	if [ -d "$SRC/.git" ]; then
		say "Updating the source in $SRC"
		if [ -n "$(git -C "$SRC" status --porcelain)" ]; then
			note "It has local changes, so it is rebuilt as it is rather than pulled."
			return
		fi
		git -C "$SRC" fetch --quiet origin "$want" || die "couldn't fetch '$want' from GitHub (offline?)"
		git -C "$SRC" checkout --quiet "$want" 2>/dev/null || git -C "$SRC" checkout --quiet -b "$want" "origin/$want"
		git -C "$SRC" merge --quiet --ff-only "origin/$want" ||
			die "$SRC has commits of its own that GitHub does not, so it cannot simply be moved forward; sort that out with git, or remove it and install again"
	else
		say "Downloading the source to $SRC"
		mkdir -p "$DATA"
		rm -rf "$SRC"
		git clone --quiet --branch "$want" "$REPO.git" "$SRC"
	fi
}

build_and_install() {
	local want="$1" v
	v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
	if ! version_ge "${v:-0}" "$NEED_GO"; then
		# Older Go downloads the version go.mod names; some distributions turn
		# that off by default.
		note "Go $v is older than $NEED_GO; Go will fetch $NEED_GO for this build."
		export GOTOOLCHAIN=auto
	fi
	fetch_source "$want"
	say "Building Atlas Commander (the first build compiles the Qt bindings and takes a few minutes)"
	make -C "$SRC" install PREFIX="$PREFIX"
}

fetch_self() {
	if [ -f "${BASH_SOURCE[0]:-}" ]; then
		cp "${BASH_SOURCE[0]}" "$1"
	else
		curl -fsSL "$RAW/release/setup.sh" -o "$1"
	fi
}

# pkg_owned names the package manager that owns an installed atlas-commander.
pkg_owned() {
	local f
	for f in /usr/bin/atlas-commander /usr/local/bin/atlas-commander; do
		[ -e "$f" ] || continue
		if have pacman && pacman -Qo "$f" >/dev/null 2>&1; then echo "pacman: sudo pacman -R atlas-commander"; return 0; fi
		if have dpkg && dpkg -S "$f" >/dev/null 2>&1; then echo "apt: sudo apt remove atlas-commander"; return 0; fi
		if have rpm && rpm -qf "$f" >/dev/null 2>&1; then echo "rpm: sudo dnf remove atlas-commander"; return 0; fi
	done
	return 1
}

# --- Actions ---------------------------------------------------------------------

do_install() {
	local want="${channel:-release}"
	say "Installing Atlas Commander ($want) on $(distro_name)"
	if pkg_owned >/dev/null; then
		die "Atlas Commander is installed by your package manager ($(pkg_owned)); update or remove it that way."
	fi
	detect_pm
	local missing
	missing="$(missing_native)"
	if [ -n "$missing" ]; then
		say "This system is missing:"
		printf '%s\n' "$missing" | sed 's/^/  - /'
		install_packages
		missing="$(missing_native)"
		[ -z "$missing" ] || die "still missing after installing packages: $(printf '%s' "$missing" | tr '\n' ',' | sed 's/,$//')"
	fi
	local old
	old="$(too_old)"
	[ -z "$old" ] || die "this system's Qt is too old to build Commander: $old"
	build_and_install "$want"
	echo
	say "Atlas Commander is installed."
	note "Open it from your app menu: search for \"Atlas\"."
	note "Update: bash $DATA/setup.sh update"
	note "Remove: bash $DATA/setup.sh uninstall"
	[ -f "$DATA/setup.sh" ] || fetch_self "$DATA/setup.sh" 2>/dev/null || true
}

do_update() {
	[ -d "$SRC/.git" ] || die "no installed copy to update under $SRC (install it with this script first)"
	local want="${channel:-$(installed_channel)}"
	say "Updating Atlas Commander ($want)"
	detect_pm
	build_and_install "$want"
	say "Up to date. Restart Atlas Commander to use the new version."
}

do_uninstall() {
	# The source and this script's own copy are about to go; a shell standing
	# in either would be left in a directory that no longer exists.
	cd "$HOME" || cd /
	if pgrep -x atlas-commander >/dev/null 2>&1; then
		die "Atlas Commander is running; close it first."
	fi
	say "Removing Atlas Commander"
	# The paths make install and the release tarball's install.sh both use.
	# Not `make uninstall`: it would run inside the checkout being deleted.
	rm -f "$PREFIX/bin/atlas-commander" "$PREFIX/bin/atlas-hook" \
		"$PREFIX/share/applications/com.atlas.Commander.desktop" \
		"$PREFIX/share/icons/hicolor/scalable/apps/com.atlas.Commander.svg" \
		"$PREFIX/share/icons/hicolor/16x16/apps/com.atlas.Commander.svg" \
		"$PREFIX/share/icons/hicolor/symbolic/apps/com.atlas.Commander-symbolic.svg"
	rm -rf "$DATA"
	have update-desktop-database && update-desktop-database "$PREFIX/share/applications" 2>/dev/null || true
	have gtk-update-icon-cache && gtk-update-icon-cache -f -t "$PREFIX/share/icons/hicolor" 2>/dev/null || true
	if [ "$purge" = 1 ]; then
		rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/atlas-commander" \
			"${XDG_DATA_HOME:-$HOME/.local/share}/atlas-commander"
		note "Settings, the database and the audit log removed."
	else
		note "Your settings are kept in ${XDG_CONFIG_HOME:-$HOME/.config}/atlas-commander (--purge removes them)."
	fi
	local owner
	if owner="$(pkg_owned)"; then
		note "A copy installed by your package manager is still there — ${owner#*: }"
	fi
	say "Atlas Commander is removed."
}

do_check() {
	detect_pm
	say "$(distro_name)"
	note "package manager: ${pm:-unknown}"
	note "Go:          $(go env GOVERSION 2>/dev/null || echo missing)"
	note "Qt 6:        $(pkg-config --modversion Qt6Widgets 2>/dev/null || echo 'no development files')"
	local missing old
	missing="$(missing_native)"
	old="$(too_old)"
	if [ -n "$old" ]; then
		say "Too old to build Commander:"
		printf '%s\n' "$old" | sed 's/^/  - /'
	elif [ -n "$missing" ]; then
		say "Would install first:"
		printf '%s\n' "$missing" | sed 's/^/  - /'
		note "${install_cmd[*]:-(no known package manager)} $packages"
	else
		say "Ready to build Atlas Commander."
	fi
	if [ -d "$SRC/.git" ]; then note "installed from $SRC ($(installed_channel))"; fi
}

case "$action" in
install) do_install ;;
update) do_update ;;
uninstall) do_uninstall ;;
check) do_check ;;
esac

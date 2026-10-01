#!/usr/bin/env bash
# Update Atlas Commander from GitHub, then reinstall. Run by the in-app
# "Update" button as:  update.sh <branch> [prefix]
#   release = what users get, beta = newest features and fixes.
#   prefix defaults to the Makefile's and justfile's own ($HOME/.local).
#
# Safety: a dirty working tree is never touched (it just rebuilds in place), the
# pull is fast-forward-only (local commits are never discarded), and a source
# that is not a git checkout falls back to rebuilding what is there. Whatever
# happens it ends by reinstalling, so the app comes back working.
#
# Exit status: 0 when the newest version of the branch is installed, 2 when the
# reinstall worked but the newest version could not be brought in (offline,
# diverged branch, uncommitted work), and anything else when the build failed.
# The app tells these three apart.
set -uo pipefail

branch="${1:-release}"
# Where to install the result. The caller passes the prefix the running copy was
# installed under, so a rebuild replaces it instead of landing somewhere else and
# leaving two Commanders for PATH to choose between. Empty means the recipes' own
# default, which is what a person running this script by hand would expect.
prefix="${2:-}"
root="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)" || exit 1
cd "$root" || exit 1

# The log goes under the user's own state directory rather than a predictable
# name in /tmp. A shared world-writable path can be squatted by another user on
# a multi-user machine, and because a failed redirect at exec is fatal to the
# shell, that would stop updates from running at all.
state="${XDG_STATE_HOME:-$HOME/.local/state}/atlas-commander"
mkdir -p "$state" 2>/dev/null || state="$(mktemp -d)"
log="$state/update.log"
exec >"$log" 2>&1
echo "Atlas Commander update, channel '$branch', $(date)"

# make and just hold the same recipes; either one does the build. make wins when
# both are there only because it is the one setup.sh prefers too.
reinstall() {
    if command -v make >/dev/null 2>&1; then
        if [ -n "$prefix" ]; then
            make -C "$root" install PREFIX="$prefix"
        else
            make -C "$root" install
        fi
    elif command -v just >/dev/null 2>&1; then
        if [ -n "$prefix" ]; then
            just --justfile "$root/justfile" --working-directory "$root" PREFIX="$prefix" install
        else
            just --justfile "$root/justfile" --working-directory "$root" install
        fi
    else
        echo "Neither make nor just is installed, so there is nothing to build with."
        return 1
    fi
}

# Rebuild what is there and report that it is not the newest: 0 from the build
# becomes 2, a build failure stays a failure.
rebuild_only() {
    reinstall || exit $?
    exit 2
}

# Not a git checkout (a download): nothing to pull, just rebuild.
if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "Source is not a git checkout; rebuilding the current source."
    rebuild_only
fi

# Never clobber uncommitted work: rebuild in place instead of pulling.
if [ -n "$(git status --porcelain)" ]; then
    echo "Working tree has local changes; skipping the GitHub pull and rebuilding in place."
    rebuild_only
fi

echo "Fetching origin..."
if ! git fetch --prune origin; then
    echo "git fetch failed (offline?); rebuilding the current source."
    rebuild_only
fi

# Switch to the channel branch (git makes a tracking branch from origin/<branch>
# on first use). If it can't switch, stay put and rebuild.
if ! git checkout "$branch"; then
    echo "Cannot switch to '$branch'; staying on $(git rev-parse --abbrev-ref HEAD) and rebuilding."
    rebuild_only
fi

# Fast-forward only: succeeds (or is a no-op) for a clean follower, refuses to
# rewrite a diverged local branch.
if git merge --ff-only "origin/$branch"; then
    echo "Updated to $(git rev-parse --short HEAD) on $branch."
    reinstall
    exit $?
fi
echo "Local '$branch' has diverged from origin/$branch; not fast-forwarding. Rebuilding current state."
rebuild_only

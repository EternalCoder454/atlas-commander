#!/usr/bin/env bash
# Update Atlas Commander from GitHub, then reinstall. Run by the in-app
# "Update" button as:  update.sh <branch> [prefix]
#   release = what users get, beta = newest features and fixes.
#   prefix defaults to the Makefile's and justfile's own ($HOME/.local).
#
# Safety: a dirty working tree is never touched (it just rebuilds in place), the
# pull is fast-forward-only (local commits are never discarded), a checkout that
# is on some other branch than release or beta is refused rather than switched
# away from, an origin that is not the Atlas Commander repository is refused,
# and a source that is not a git checkout falls back to rebuilding what is
# there. Otherwise it ends by reinstalling, so the app comes back working.
#
# Exit status: 0 when the newest version of the branch is installed, 2 when the
# reinstall worked but the newest version could not be brought in (offline,
# diverged branch, uncommitted work), and 1 when anything failed, including the
# build. GNU make exits 2 on a build error, so every build failure is turned
# into 1 here: 2 is only ever this script's own "rebuilt but not newest".
# The app tells these three apart.
#
# The whole body is a function called on the last line. git checkout and merge
# can rewrite this very file, and bash reads a script as it runs; with
# everything parsed before anything executes, a rewrite cannot corrupt the run.
set -uo pipefail

main() {
    local branch="${1:-release}"
    # Where to install the result. The caller passes the prefix the running copy
    # was installed under, so a rebuild replaces it instead of landing somewhere
    # else and leaving two Commanders for PATH to choose between. Empty means the
    # recipes' own default, which is what a person running this script by hand
    # would expect.
    local prefix="${2:-}"
    local root
    root="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)" || exit 1
    cd "$root" || exit 1

    # The log goes under the user's own state directory rather than a predictable
    # name in /tmp. A shared world-writable path can be squatted by another user
    # on a multi-user machine, and because a failed redirect at exec is fatal to
    # the shell, that would stop updates from running at all.
    local state="${XDG_STATE_HOME:-$HOME/.local/state}/atlas-commander"
    mkdir -p "$state" 2>/dev/null || state="$(mktemp -d)"
    local log="$state/update.log"
    exec >"$log" 2>&1
    echo "Atlas Commander update, channel '$branch', $(date)"

    # make and just hold the same recipes; either one does the build. make wins
    # when both are there only because it is the one setup.sh prefers too.
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

    # Rebuild what is there and report that it is not the newest: success from
    # the build becomes 2, and any build failure becomes 1 (never make's own 2).
    rebuild_only() {
        reinstall || exit 1
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

    # A checkout on its own branch is somebody's work; switching to a channel
    # would leave it behind. The app says the same thing when it checks.
    local current
    current="$(git rev-parse --abbrev-ref HEAD 2>/dev/null)"
    case "$current" in
        release | beta) ;;
        *)
            echo "Your checkout is on another branch ($current), so it was left alone. Switch it to release or beta to update."
            exit 1
            ;;
    esac

    # Only ever pull from the real repository: a checkout whose origin points
    # somewhere else must not have that code built and installed by "Update".
    local origin
    origin="$(git remote get-url origin 2>/dev/null)"
    case "$origin" in
        https://github.com/EternalCoder454/atlas-commander | https://github.com/EternalCoder454/atlas-commander.git | https://github.com/EternalCoder454/atlas-commander/ | \
            ssh://git@github.com/EternalCoder454/atlas-commander | ssh://git@github.com/EternalCoder454/atlas-commander.git | \
            git@github.com:EternalCoder454/atlas-commander | git@github.com:EternalCoder454/atlas-commander.git) ;;
        *)
            echo "This checkout's origin is not the Atlas Commander repository on GitHub, so it was not updated."
            exit 1
            ;;
    esac

    echo "Fetching origin..."
    if ! git fetch --prune origin; then
        echo "git fetch failed (offline?); rebuilding the current source."
        rebuild_only
    fi

    # Switch to the channel branch (git makes a tracking branch from
    # origin/<branch> on first use). If it can't switch, stay put and rebuild.
    if ! git checkout "$branch"; then
        echo "Cannot switch to '$branch'; staying on $(git rev-parse --abbrev-ref HEAD) and rebuilding."
        rebuild_only
    fi

    # Fast-forward only: succeeds (or is a no-op) for a clean follower, refuses
    # to rewrite a diverged local branch.
    if git merge --ff-only "origin/$branch"; then
        echo "Updated to $(git rev-parse --short HEAD) on $branch."
        reinstall || exit 1
        exit 0
    fi
    echo "Local '$branch' has diverged from origin/$branch; not fast-forwarding. Rebuilding current state."
    rebuild_only
}

main "$@"
# main always exits itself; this line is the backstop that stops bash from
# reading on past the function if a later edit makes it return instead.
# shellcheck disable=SC2317
exit $?

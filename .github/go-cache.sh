#!/usr/bin/env bash
# Keep a restored Go build cache usable across CI runs.
#
#   go-cache.sh hold    right after the cache is restored, before any go command
#   go-cache.sh prune   as the job's last step, before the cache is saved
#
# Why: Go trims its own build cache, deleting every file it has not used in five
# days, at the end of a go command, at most once a day. The trim is by file date
# alone, and one kind of use never updates a date: when a build produces an
# output byte-for-byte identical to one already cached, Go points the new entry
# at the old file and leaves the old file's date alone. So on a cache restored
# after more than five days, the trim at the end of the build deletes outputs the
# build has just started to rely on, the cache is saved like that, and every run
# after it misses and recompiles. (cmd/go/internal/cache: copyFile returns early
# without markUsed.)
#
# It happened on the gotk4 0.4.1 upgrade. The files lost were each package's
# list of sources and the cgo-generated sources Go keeps for vet, identical to
# the old gotk4's; without them, go test's vet step reran cgo over glib, gio and
# gtk. The race job then took 12 minutes instead of 2 on every run, because its
# cache was restored under an exact key, and such a cache is never saved again.
#
# So Go's trim is held off for the job, and this does it instead, by reference
# rather than by date: an entry is kept if this job used or created it, and an
# output is kept if a kept entry names it. The saved cache is then exactly what
# this job needed, however old any of it is.
set -euo pipefail

cache=$(go env GOCACHE)
command -v cygpath >/dev/null && cache=$(cygpath -u "$cache") # MSYS2
mkdir -p "$cache"

# Restored files are set to this date, and anything Go uses or writes during
# the job moves off it: Go redates a file it reads once it is an hour old.
held='2000-01-01 00:00 UTC'
since_held='2000-01-02 00:00 UTC'

files() { find "$cache" -mindepth 2 -maxdepth 2 -name "*-$1" "${@:2}"; }

case ${1-} in
hold)
    files a -exec touch -d "$held" {} +
    files d -exec touch -d "$held" {} +
    date +%s >"$cache/trim.txt" # Go skips its trim for a day after this
    ;;
prune)
    before=$(files a | wc -l)
    files a ! -newermt "$since_held" -delete
    # Each entry is "v1 <action> <output> <size> <time>"; outputs are <output>-d.
    keep=$(mktemp)
    files a -exec cat {} + | awk '{ print $3 "-d" }' | sort -u >"$keep"
    files d -printf '%f\n' | sort | comm -23 - "$keep" |
        while read -r out; do rm -rf -- "${cache:?}/${out:0:2}/$out"; done
    rm -f "$keep"
    echo "kept $(files a | wc -l) of $before entries, $(files d | wc -l) outputs"
    ;;
*)
    echo "usage: $0 hold|prune" >&2
    exit 2
    ;;
esac

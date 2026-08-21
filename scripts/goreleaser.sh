#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

if [ ! -e .git ]; then
    snapshot=false
    for arg in "$@"; do
        [ "$arg" = --snapshot ] && snapshot=true
    done
    if [ "${1:-}" != check ] && [ "$snapshot" != true ]; then
        echo "publishing a release requires the primary checkout" >&2
        exit 1
    fi

    git_dir=$(jj git root)
    set -- env GIT_DIR="$git_dir" GIT_WORK_TREE="$PWD" goreleaser "$@"
else
    set -- goreleaser "$@"
fi

exec env \
    -u NIX_CFLAGS_COMPILE \
    -u NIX_LDFLAGS \
    -u SDKROOT \
    -u DEVELOPER_DIR \
    "$@"

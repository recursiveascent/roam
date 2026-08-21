#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

version=$(cat VERSION)
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
    echo "VERSION must contain a bare semantic version, got '$version'" >&2
    exit 1
}

./scripts/goreleaser.sh check
./scripts/goreleaser.sh release --clean --snapshot

snapshot_version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' dist/metadata.json)
[ -n "$snapshot_version" ] || {
    echo "could not read snapshot version from dist/metadata.json" >&2
    exit 1
}

./scripts/check-release-artifacts.sh dist "$snapshot_version"

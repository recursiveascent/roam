#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <artifact-dir> <version>" >&2
    exit 2
fi

dir=$1
version=$2

[ -f "$dir/checksums.txt" ] || {
    echo "missing $dir/checksums.txt" >&2
    exit 1
}

(
    CDPATH='' cd "$dir"
    shasum -a 256 -c checksums.txt
)

set -- "$dir"/roam_*_darwin_*.tar.gz
[ "$#" -eq 2 ] || {
    echo "expected two Darwin archives in $dir" >&2
    exit 1
}

set -- "$dir"/roam-*.tar.gz
[ "$#" -eq 1 ] && [ -f "$1" ] || {
    echo "expected one source archive in $dir" >&2
    exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
host_arch=$(go env GOARCH)
ran_host=false

for arch in amd64 arm64; do
    set -- "$dir"/roam_*_darwin_"$arch".tar.gz
    [ "$#" -eq 1 ] && [ -f "$1" ] || {
        echo "expected one darwin/$arch archive in $dir" >&2
        exit 1
    }
    archive=$1

    for entry in README.md CHANGELOG.md COPYING roam; do
        tar tzf "$archive" | grep -Fx "$entry" >/dev/null || {
            echo "$archive is missing $entry" >&2
            exit 1
        }
    done

    extract="$tmp/$arch"
    mkdir "$extract"
    tar xzmf "$archive" -C "$extract"
    binary="$extract/roam"

    case "$arch:$(file "$binary")" in
        amd64:*x86_64*) ;;
        arm64:*arm64*) ;;
        *)
            echo "$binary has the wrong architecture" >&2
            file "$binary" >&2
            exit 1
            ;;
    esac

    if otool -L "$binary" | grep -F /nix/store >/dev/null; then
        echo "$binary links against /nix/store" >&2
        otool -L "$binary" >&2
        exit 1
    fi

    if [ "$arch" = "$host_arch" ]; then
        output=$("$binary" --version)
        [ "$output" = "roam $version" ] || {
            echo "$binary reported '$output', want 'roam $version'" >&2
            exit 1
        }
        ran_host=true
    fi
done

[ "$ran_host" = true ] || {
    echo "no archive matched host architecture $host_arch" >&2
    exit 1
}

echo "release artifacts: OK"

#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
export HOMEBREW_NO_AUTO_UPDATE=1

version=$(cat VERSION)
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
    echo "VERSION must contain a bare semantic version, got '$version'" >&2
    exit 1
}
tag="v$version"
repo=recursiveascent/roam

draft=$(gh release view "$tag" -R "$repo" --json isDraft --jq .isDraft)
[ "$draft" = false ] || {
    echo "$tag is still a draft release" >&2
    exit 1
}
prerelease=$(gh release view "$tag" -R "$repo" --json isPrerelease --jq .isPrerelease)
[ "$prerelease" = false ] || {
    echo "$tag is still a prerelease" >&2
    exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
gh release download "$tag" -R "$repo" --dir "$tmp"
./scripts/check-release-artifacts.sh "$tmp" "$version"

gh api repos/recursiveascent/homebrew-tap/contents/Formula/roam.rb --jq .content \
    | tr -d '\n' \
    | base64 -d > "$tmp/roam.rb"
ruby -c "$tmp/roam.rb"

tap=$(brew --repo recursiveascent/tap 2>/dev/null || true)
if [ -z "$tap" ]; then
    brew tap recursiveascent/tap
    tap=$(brew --repo recursiveascent/tap)
else
    [ -z "$(jj -R "$tap" diff --summary)" ] || {
        echo "Homebrew tap working copy has local changes: $tap" >&2
        exit 1
    }
    jj -R "$tap" git fetch --remote origin
    jj -R "$tap" rebase -r @ -d main@origin
fi

[ -f "$tap/Formula/roam.rb" ] || {
    echo "local tap does not contain Formula/roam.rb" >&2
    exit 1
}

if brew list --formula roam >/dev/null 2>&1; then
    brew reinstall recursiveascent/tap/roam
else
    brew install recursiveascent/tap/roam
fi

binary="$(brew --prefix)/bin/roam"
output=$("$binary" --version)
[ "$output" = "roam $version" ] || {
    echo "$binary reported '$output', want 'roam $version'" >&2
    exit 1
}
brew test recursiveascent/tap/roam

echo "published release: OK"

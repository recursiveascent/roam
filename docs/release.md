# Release process

Releases are cut by tagging a commit on `main`. GoReleaser builds the
artifacts and publishes the GitHub release; the release workflow then updates
the Homebrew tap formula automatically.

## Prerequisites

- GoReleaser is provided by `flake.nix`; run commands through `nix develop`.
- The `recursiveascent/roam` Actions secret `CACHIX_AUTH_TOKEN` must contain a
  Cachix auth token that can push to the `recursiveascent` cache:

  ```
  gh secret set CACHIX_AUTH_TOKEN -R recursiveascent/roam
  ```

- A fine-grained PAT with **contents: write** on
  `recursiveascent/homebrew-tap` must exist as the repo secret `TAP_TOKEN`:

  ```
  gh secret set TAP_TOKEN -R recursiveascent/roam
  ```

  The default `GITHUB_TOKEN` is scoped to the roam repository and cannot
  update the tap.

## Steps

1. **Edit `CHANGELOG.md`** — add a `## <version>` section above the last
   release. Keep entries user-facing and concrete.

2. **Bump `VERSION`** — the file contains the bare version (`0.1.0`, no `v`
   prefix). The release workflow verifies that `v$(cat VERSION)` equals the
   tag name.

3. **Validate the release config and build locally:**

   ```
   nix develop -c goreleaser check
   nix develop -c env -u NIX_CFLAGS_COMPILE -u NIX_LDFLAGS -u SDKROOT -u DEVELOPER_DIR goreleaser release --clean --snapshot
   ```

4. **Run checks:**

   ```
   nix develop -c make check
   nix flake check
   nix build
   ```

5. **Describe the release revision** with a `Prepare <version> release`
   message. Keep the VERSION bump and release configuration atomic so the tag
   points at a revision whose VERSION matches.

6. **Move `main` to the release revision and push:**

   ```
   jj bookmark set main -r @
   jj git push --remote origin --bookmark main
   ```

7. **Tag `main` and push the tag:**

   ```
   jj tag set v<version> -r main
   jj git push --remote origin --tag v<version>
   ```

Pushing a `v*` tag triggers the Release workflow.

## What the workflow does

`.github/workflows/release.yml` runs on a pushed `v*` tag:

1. Verifies the tag matches `VERSION`.
2. Runs GoReleaser on macOS to publish amd64 and arm64 Darwin archives, a
   source archive, and `checksums.txt`.
3. Renders `Formula/roam.rb` as a macOS-only source-build formula and commits
   it to `recursiveascent/homebrew-tap` through the Contents API.

The Homebrew formula builds from source with Go. GoReleaser's Homebrew
integrations are not used; the explicit formula keeps the tap update visible
and matches litefind's release process.

## Version resolution

`roam --version` resolves in this order:

1. `main.versionOverride`, injected by GoReleaser and Nix.
2. The module version stamped by the Go toolchain, covering
   `go install github.com/recursiveascent/roam@v<version>`.
3. The embedded `VERSION` file.

## Verifying a release

After the workflow completes:

- The GitHub release should list two Darwin archives, the source archive, and
  `checksums.txt`.
- Inspect the tap formula:

  ```
  gh api repos/recursiveascent/homebrew-tap/contents/Formula/roam.rb --jq .content | base64 -d
  ```

- Install and verify:

  ```
  brew reinstall recursiveascent/tap/roam
  roam --version
  ```

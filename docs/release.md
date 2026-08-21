# Release process

Releases are cut by tagging a commit on `main`. GoReleaser publishes the
GitHub release; the release workflow then updates the Homebrew tap formula.

## Prerequisites

- Run release commands through the Nix development shell. `flake.nix` provides
  Go and GoReleaser.
- The `recursiveascent/roam` Actions secret `CACHIX_AUTH_TOKEN` must contain a
  token that can push to the `recursiveascent` Cachix cache:

  ```
  gh secret set CACHIX_AUTH_TOKEN -R recursiveascent/roam
  ```

- The Actions secret `TAP_TOKEN` must contain a fine-grained PAT with
  **Contents: write** access to `recursiveascent/homebrew-tap`. Fine-grained
  PATs are created in GitHub's web UI, not with `gh`; store it afterward:

  ```
  gh secret set TAP_TOKEN -R recursiveascent/roam
  ```

- The local `gh` token needs the `workflow` scope to push commits that change
  workflow files:

  ```
  gh auth refresh -h github.com -s workflow
  ```

## Preflight

Check authentication and required secrets:

```
gh auth status -h github.com
gh secret list -R recursiveascent/roam
```

The secret list must contain `CACHIX_AUTH_TOKEN` and `TAP_TOKEN`.

Fetch before preparing the release so remote movement and authentication
failures are found early:

```
jj git fetch --remote origin
```

If an SSH remote fails because no key is available, switch this checkout to
GitHub CLI's HTTPS credentials:

```
jj git remote set-url origin https://github.com/recursiveascent/roam.git
jj git fetch --remote origin
```

Confirm that the working revision contains only the intended release changes:

```
jj st
jj log -n 4
```

Release preparation can happen in an isolated jj workspace. Publishing cannot:
`scripts/goreleaser.sh` refuses a non-snapshot release from a secondary jj
workspace because its shared Git HEAD points at the primary checkout.

## Prepare and validate

1. Add a user-facing section to `CHANGELOG.md`.

2. Set `VERSION` to the bare semantic version, for example `0.2.0` rather than
   `v0.2.0`.

3. Run the complete local release gate:

   ```
   nix develop -c make release-check
   ```

   `release-check` validates the GoReleaser configuration, builds a clean
   snapshot, verifies checksums and archive contents, checks both Mach-O
   architectures, runs the host binary, and rejects dynamic-library references
   into `/nix/store`.

4. Run the normal project checks:

   ```
   nix develop -c make check
   nix flake check
   nix build
   ```

5. Describe the release revision atomically:

   ```
   jj desc -m "Prepare $(cat VERSION) release"
   ```

The tag must point at the revision containing the matching `VERSION`.

## Publish

From the primary checkout, move and push `main`:

```
jj bookmark set main -r @
jj git push --remote origin --bookmark main
```

Create the tag locally, then inspect both references before pushing it:

```
version=$(cat VERSION)
jj tag set "v$version" -r main
jj show main
jj tag list
jj git push --remote origin --tag "v$version"
```

Pushing a `v*` tag triggers `.github/workflows/release.yml`.

## Monitor GitHub Actions

List the runs created by the main and tag pushes:

```
gh run list -R recursiveascent/roam --limit 5
```

Attach to each CI and Release run and propagate failure through the command's
exit status:

```
gh run watch <run-id> -R recursiveascent/roam --exit-status
```

The Release workflow:

1. Verifies that the tag equals `v$(cat VERSION)`.
2. Builds amd64 and arm64 Darwin archives, a source archive, and
   `checksums.txt`.
3. Publishes the GitHub release.
4. Renders and commits `Formula/roam.rb` to
   `recursiveascent/homebrew-tap` through the Contents API.

## Verify the published release

After both workflows pass, run:

```
make release-verify
```

`release-verify`:

- Confirms the release is published rather than draft or prerelease.
- Downloads every asset and verifies `checksums.txt`.
- Repeats archive, architecture, version, and linkage checks against the
  published binaries.
- Downloads and syntax-checks the Homebrew formula.
- Refreshes only the jj-backed `recursiveascent/tap` checkout.
- Installs or reinstalls the formula and runs `brew test`.
- Invokes `$(brew --prefix)/bin/roam` directly so another `roam` earlier on
  `PATH` cannot hide the Homebrew binary.

The script always sets `HOMEBREW_NO_AUTO_UPDATE=1`. Release verification must
never run the global `brew update`; only the project tap is fetched.

## Release scripts

- `scripts/goreleaser.sh` removes `NIX_CFLAGS_COMPILE`, `NIX_LDFLAGS`,
  `SDKROOT`, and `DEVELOPER_DIR` before invoking GoReleaser. A normal Nix
  package should link against its Nix closure, but standalone release binaries
  must link only against system macOS libraries.
- `scripts/check-release-artifacts.sh` contains the checks shared by local
  snapshots and published releases.
- `scripts/release-check.sh` is the pre-tag local gate and CI release-package
  check.
- `scripts/release-verify.sh` is the post-tag GitHub and Homebrew check.

## Version resolution

`roam --version` resolves in this order:

1. `main.versionOverride`, injected by GoReleaser and Nix.
2. The module version stamped by the Go toolchain, covering
   `go install github.com/recursiveascent/roam@v<version>`.
3. The embedded `VERSION` file.

## Troubleshooting

### Release binary references `/nix/store`

Run `nix develop -c make release-check`. Always invoke GoReleaser through
`scripts/goreleaser.sh`; direct execution inside `nix develop` leaks
architecture-specific Nix compiler and SDK paths into CGO binaries.

### GoReleaser says the jj workspace is not a Git repository

Use `scripts/goreleaser.sh`. It supplies the shared Git directory and current
workspace tree for `check` and snapshot releases. Publishing remains restricted
to the primary checkout.

### Fetch or push fails with `Permission denied (publickey)`

Switch `origin` to HTTPS as shown in Preflight. Do not wait until after moving
`main` to discover the authentication failure.

### The tap formula exists on GitHub but Homebrew cannot find it

The local tap's remote bookmark may be current while its jj working copy is
stale. Run `make release-verify`; it fetches the tap and rebases its empty
working-copy revision onto `main@origin`. It aborts rather than disturb local tap
changes.

### Homebrew tries to update unrelated repositories

Ensure `HOMEBREW_NO_AUTO_UPDATE=1` is exported. The release verification script
sets it itself. Do not use `brew update` to refresh this tap.

### The tag workflow fails before publishing

Compare the exact tag with `VERSION`:

```
jj tag list
cat VERSION
```

The required relationship is `tag == v$(cat VERSION)` and both `main` and the
tag must point at the same revision.

### The installed command is not the Homebrew build

Check for PATH shadowing and invoke the formula directly:

```
command -v roam
"$(brew --prefix)/bin/roam" --version
```

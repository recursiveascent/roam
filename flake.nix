{
  description = "roam — event-driven ssh supervisor for macOS (autossh successor)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # roam is macOS-only: cgo against Network.framework, build-tagged darwin.
      supportedSystems = [
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;

      version = nixpkgs.lib.removeSuffix "\n" (builtins.readFile ./VERSION);
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          lib = pkgs.lib;
        in
        {
          roam = pkgs.buildGoModule {
            pname = "roam";
            inherit version;
            src = self;

            # No vendored deps; go.mod has no requires. A fixed-output hash is
            # still required by buildGoModule, and empty deps make it trivial.
            vendorHash = null;

            ldflags = [
              "-s"
              "-w"
              "-X 'main.versionOverride=${version}'"
            ];

            # Network.framework is a system framework present in the default
            # macOS SDK; -framework Network links it without extra buildInputs.
            # cgo is enabled by default when the stdenv supports it (it does on
            # darwin). No CGO_LDFLAGS override is needed.

            doCheck = true;

            meta = {
              description = "Event-driven ssh supervisor for macOS, a modern successor to autossh";
              homepage = "https://github.com/recursiveascent/roam";
              license = lib.licenses.mit;
              mainProgram = "roam";
              platforms = supportedSystems;
            };
          };

          default = self.packages.${system}.roam;
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              gotools
              goreleaser
            ];

            # `go build` resolves the SDK sysroot the same way it does for the
            # package build; no manual framework paths are needed.
            shellHook = ''
              echo "roam dev shell — go $(go version 2>/dev/null | awk '{print $3}')"
            '';
          };
        }
      );

      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}

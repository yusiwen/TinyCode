{
  # TinyCode — AI coding agent in Go.
  #
  # This flake provides a reproducible Go development environment
  # (`devShells.default`): Go toolchain, gopls, gofumpt, git. Activate it
  # with `direnv` (see .envrc) or `nix develop`.
  #
  # The canonical build/test workflow is driven by the Makefile
  # (`make build`, `make test`, `make lint`), which works unchanged inside
  # the dev shell. The Go toolchain satisfies the `go 1.24.2` requirement.
  description = "TinyCode — AI coding agent written in Go";

  inputs = {
    # Pinned to a known-good nixos-unstable snapshot (default Go 1.26.x).
    nixpkgs.url = "github:NixOS/nixpkgs/624af665418d3c65d544145b4d34ad696439570e";
    flake-utils.url = "github:numtide/flake-utils/11707dc2f618dd54ca8739b309ec4fc024de578b";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs { inherit system; };
    in {
      devShells.default = pkgs.mkShell {
        # Go toolchain + the tools `make lint`/the LSP client rely on.
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
          pkgs.gopls
          pkgs.gofumpt
        ];

        shellHook = ''
          echo "[tinycode] nix dev shell  —  $(go version)"
          # Use the Nix-provided toolchain as-is (no auto-download from go.dev).
          export GOTOOLCHAIN=local
          # Drop any host GOROOT (e.g. /opt/go) so the Nix Go resolves its own
          # stdlib/tools — otherwise the compile tool versions mismatch.
          unset GOROOT
          # Shared, persistent module/build caches; respect a pre-set value.
          if [ -z "$GOMODCACHE" ]; then export GOMODCACHE="$HOME/.cache/go-mod"; fi
          if [ -z "$GOCACHE" ]; then export GOCACHE="$HOME/.cache/go-build"; fi
          echo "[tinycode] GOMODCACHE=$GOMODCACHE"
          echo "[tinycode] GOCACHE=$GOCACHE"
        '';
      };
    });
}

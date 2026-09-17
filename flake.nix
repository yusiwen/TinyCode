{
  # TinyCode — AI coding agent in Go.
  #
  # This flake provides a reproducible Go development environment
  # (`devShells.default`): Go toolchain, gopls, gofumpt, git. Activate it
  # with `direnv` (see .envrc) or `nix develop`.
  #
  # The canonical build/test workflow is driven by the Makefile
  # (`make build`, `make test`, `make lint`), which works unchanged inside
  # the dev shell. The dev shell provides the same Go line as CI (1.27), so the
  # `go 1.27` directive in go.mod is satisfied everywhere.
  description = "TinyCode — AI coding agent written in Go";

  inputs = {
    # Pinned to a known-good nixos-unstable snapshot. Its `go` attribute is
    # still the 1.26 line, so the 1.27 toolchain is selected explicitly below.
    nixpkgs.url = "github:NixOS/nixpkgs/b1b875982b17dabde9b4a37f3e229e74913e6db3";
    flake-utils.url = "github:numtide/flake-utils/11707dc2f618dd54ca8739b309ec4fc024de578b";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs { inherit system; };
    in {
      devShells.default = pkgs.mkShell {
        # Go toolchain + the tools `make lint`/the LSP client rely on.
        # go_1_27 (not `go`) is deliberate: CI, go.mod and this shell must all
        # use the 1.27 line, and nixos-unstable still defaults to Go 1.26.
        nativeBuildInputs = [
          pkgs.go_1_27
          pkgs.git
          pkgs.gopls
          pkgs.gofumpt
        ];

        shellHook = ''
          echo "[tinycode] nix dev shell  —  $(go version)"
          # Use the Nix-provided toolchain as-is (no auto-download from go.dev).
          export GOTOOLCHAIN=local
          # The project imports no cgo and `make build` builds with CGO_ENABLED=0.
          # Without this the shell's cgo-enabled default makes a plain
          # cross-target `go build ./...` (what the CI matrix runs) try to
          # compile runtime/cgo with the host toolchain and fail.
          export CGO_ENABLED=0
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

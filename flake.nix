{
  description = "Networked picture frame: LVGL frontend + Go Nextcloud backend";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  # Nix's remaining job here is the development shell. It does not build the
  # Raspberry Pi artifacts and never runs on the Pi itself.
  #
  # It used to cross-compile them through pkgsCross. That was a mistake in
  # practice: cross-built derivations are largely absent from cache.nixos.org,
  # so CI rebuilt binutils, gcc and glibc from source on every miss, and the
  # run took the better part of an hour. The Pi build now happens in
  # .github/workflows/build.yml with Debian's own cross toolchain, inside the
  # same Bookworm release the frames run — which pins the glibc the binaries
  # are linked against, the one property the Nix build was really buying.
  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let pkgs = nixpkgs.legacyPackages.${system}; in
      {
        devShells.default = pkgs.mkShell {
          # The Mac build is done locally from this shell (or plain Homebrew);
          # there is no macOS job in CI.
          packages = with pkgs; [
            cmake
            ninja
            pkg-config
            SDL2
            go
            gopls
            clang-tools
            shellcheck
          ];
        };
      });
}

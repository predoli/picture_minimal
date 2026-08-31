{
  description = "Networked picture frame: LVGL frontend + Go Nextcloud backend";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  # Nix's only job here is making the Raspberry Pi build reproducible. It never
  # runs on the Pi itself: a nixpkgs evaluation costs hundreds of megabytes of
  # RSS, which is most of a Zero 2 W. The frame runs plain Debian with Tailscale
  # from its own apt repository.
  outputs = { self, nixpkgs, flake-utils }:
    let
      version = "0.1.0";

      # The frontend, parameterised over a package set so the same expression
      # serves both the native Linux build (CI's compile check) and the aarch64
      # cross build (the actual artifact).
      mkFrontend = pkgs: display:
        pkgs.stdenv.mkDerivation {
          pname = "picture-frontend";
          inherit version;
          src = ./.;

          nativeBuildInputs = [ pkgs.cmake pkgs.ninja pkgs.pkg-config ];
          buildInputs =
            if display == "drm" then [ pkgs.libdrm ] else [ pkgs.SDL2 ];

          cmakeFlags = [ "-DFRAME_DISPLAY=${display}" ];

          meta.description = "LVGL picture frame frontend (${display})";
        };

      mkBackend = pkgs: goarch:
        pkgs.buildGoModule {
          pname = "picture-backend";
          inherit version;
          src = ./backend;

          # Refreshed with:
          #   nix build .#backend-aarch64 2>&1 | grep 'got:'
          vendorHash = null; # set once dependencies settle; null vendors nothing

          # CGO stays off so the aarch64 build is a plain static cross-compile.
          # The HEIC decoder is WebAssembly under wazero rather than a C library,
          # which is precisely what keeps this true.
          env = {
            CGO_ENABLED = "0";
            GOOS = "linux";
            GOARCH = goarch;
          };

          subPackages = [ "cmd/picture-backend" ];

          meta.description = "Nextcloud sync backend for the picture frame";
        };
    in
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        # Cross set targeting the Pi. The Zero 2 W runs 64-bit Raspberry Pi OS.
        cross = import nixpkgs {
          inherit system;
          crossSystem = { config = "aarch64-unknown-linux-gnu"; };
        };
      in
      {
        packages = {
          # Artifacts CI publishes.
          frontend-aarch64 = mkFrontend cross "drm";
          backend-aarch64 = mkBackend cross "arm64";

          # Fast compile check on the CI runner's own architecture.
          frontend-sdl = mkFrontend pkgs "sdl";

          default = self.packages.${system}.backend-aarch64;
        };

        devShells.default = pkgs.mkShell {
          # The Mac build is done locally with this shell (or plain Homebrew);
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

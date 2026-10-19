{
  description = "Painted Wolf Code Linux package and development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    bun2nix = {
      url = "github:nix-community/bun2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, rust-overlay, bun2nix }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
      packagesFor = system: import nixpkgs {
        inherit system;
        overlays = [ rust-overlay.overlays.default ];
      };
    in {
      packages = eachSystem (system:
        let
          pkgs = packagesFor system;
          converter = bun2nix.packages.${system}.default;
        in {
          engine = pkgs.callPackage ./packaging/nix/engine.nix {
            source = self;
          };
          frontend = pkgs.callPackage ./packaging/nix/frontend.nix {
            source = self;
            bun2nix = converter;
          };
          bun-lock = pkgs.runCommand "painted-wolf-bun-lock" { nativeBuildInputs = [ converter ]; } ''
            cp ${self}/lycaon-den/bun.lock bun.lock
            bun2nix -o "$out"
          '';
        });
      devShells = eachSystem (system:
        let pkgs = packagesFor system;
        in {
          default = pkgs.callPackage ./packaging/nix/development.nix {
            source = self;
          };
        });
    };
}

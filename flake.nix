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
      hasNixOSModule = builtins.pathExists ./packaging/nix/nixos.nix;
      hasNixOSVM = hasNixOSModule && builtins.pathExists ./packaging/nix/module-vm.nix;
      packagesFor = system: import nixpkgs {
        inherit system;
        overlays = [ rust-overlay.overlays.default ];
      };
    in {
      nixosModules = nixpkgs.lib.optionalAttrs hasNixOSModule {
        default = import ./packaging/nix/nixos.nix {
          packageFor = system: self.packages.${system}.default;
        };
      };
      checks = eachSystem (system: nixpkgs.lib.optionalAttrs hasNixOSModule {
        nixos-module = (packagesFor system).callPackage ./packaging/nix/module-check.nix { inherit nixpkgs; };
      });
      packages = eachSystem (system:
        let
          pkgs = packagesFor system;
          converter = bun2nix.packages.${system}.default;
          frontend = pkgs.callPackage ./packaging/nix/frontend.nix {
            source = self;
            bun2nix = converter;
          };
          resources = pkgs.callPackage ./packaging/nix/resources.nix { source = self; inherit frontend; };
          bialy = pkgs.callPackage ./packaging/nix/bialy.nix { source = self; };
          document-core = pkgs.callPackage ./packaging/nix/document-core.nix { source = self; };
          shell = pkgs.callPackage ./packaging/nix/shell.nix { source = self; inherit frontend; };
          bound-engine = pkgs.callPackage ./packaging/nix/engine.nix {
            source = self;
            scannerIdentityFile = "${resources}/scanner-identity.base64";
            gsettingsExecutable = "${pkgs.glib.bin}/bin/gsettings";
          };
        in {
          engine = pkgs.callPackage ./packaging/nix/engine.nix {
            source = self;
          };
          inherit frontend resources shell bialy document-core bound-engine;
          default = pkgs.callPackage ./packaging/nix/application.nix {
            source = self;
            engine = bound-engine;
            inherit shell bialy document-core resources;
          };
          browser = resources.inputs.browser;
          gitengine = resources.inputs.gitengine;
          ocr = resources.inputs.ocr;
          decision-resources = resources.inputs.decision;
          scanner-original = resources.inputs.scannerOriginal;
          notices = resources.notices;
          notice-tools = resources.notices.cargoAbout;
          bun-lock = pkgs.runCommand "painted-wolf-bun-lock" { nativeBuildInputs = [ converter ]; } ''
            cp ${self}/lycaon-den/bun.lock bun.lock
            bun2nix -o "$out"
          '';
        } // nixpkgs.lib.optionalAttrs hasNixOSVM {
          nixos-gnome-vm = import ./packaging/nix/module-vm.nix {
            inherit nixpkgs system;
            package = self.packages.${system}.default;
            desktop = "gnome";
          };
          nixos-plasma-vm = import ./packaging/nix/module-vm.nix {
            inherit nixpkgs system;
            package = self.packages.${system}.default;
            desktop = "plasma";
          };
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

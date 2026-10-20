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
          desktop-shell-check = shell.overrideAttrs (_: {
            doCheck = true;
            cargoCheckType = "release";
            nativeCheckInputs = [ pkgs.python3 ];
            preCheck = ''
              # A filter with no matching tests must never qualify this component.
              (
                cd lycaon-den/src-tauri
                cargo test --locked --offline --quiet --profile release \
                  --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget} \
                  --package painted-wolf-code --lib \
                  windows::linux_desktop_settings::portal::tests \
                  -- --list --format terse > "$TMPDIR/desktop-observer-tests"
              )
              python3 - "$TMPDIR/desktop-observer-tests" <<'OBSERVER_TESTS'
              from pathlib import Path
              import sys
              path = Path(sys.argv[1])
              if path.stat().st_size > 16384:
                  raise SystemExit('Desktop observer test registry exceeds its bound')
              rows = path.read_text().splitlines()
              prefix = 'windows::linux_desktop_settings::portal::tests::'
              expected = {prefix + name + ': test' for name in (
                  'managed_shutdown_joins_the_worker_and_is_idempotent',
                  'dropping_one_observer_does_not_stop_another_owner')}
              actual = [row for row in rows if row and row != '2 tests, 0 benchmarks']
              if len(actual) != 2 or set(actual) != expected:
                  raise SystemExit('Desktop observer lifecycle test identities differ')
              print('Both fixed desktop observer lifecycle tests are registered')
              OBSERVER_TESTS
            '';
            cargoTestFlags = [ "--package" "painted-wolf-code" "--lib"
              "windows::linux_desktop_settings::portal::tests" ];
          });
          desktop-settings-observer = pkgs.callPackage ./packaging/nix/desktop-settings.nix { source = self; };
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
          scanner-finalized = resources.scanner;
          notices = resources.notices;
          notice-tools = resources.notices.cargoAbout;
          nix-workflow-fixture = pkgs.writeText "painted-wolf-nix-workflow-inputs.json" (builtins.toJSON {
            schema = 1;
            bash = "${pkgs.bash}/bin/bash";
            coreutils = "${pkgs.coreutils}/bin/mkdir";
            stdenv = "${pkgs.stdenv}";
          });
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

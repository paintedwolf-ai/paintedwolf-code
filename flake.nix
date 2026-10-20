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
          desktopTests = [
            "windows::linux_desktop_settings::portal::tests::managed_shutdown_joins_the_worker_and_is_idempotent"
            "windows::linux_desktop_settings::portal::tests::dropping_one_observer_does_not_stop_another_owner"
            "child_credentials::tests::external_exec_keeps_only_the_real_group"
            "child_credentials::tests::generic_shell_permission_is_unavailable_on_linux"
            "files::linux_desktop_open::tests::only_unanswered_file_manager_interfaces_allow_an_alternative"
            "files::linux_desktop_open::tests::local_file_uris_preserve_reserved_and_unicode_bytes"
            "files::linux_path_picker::tests::default_size_fits_logical_workarea_including_two_hundred_percent_scale"
            "files::linux_path_picker::tests::selections_retain_folder_read_and_save_authority"
            "files::linux_path_picker::tests::options_refuse_nonlocal_save_names_and_filter_patterns"
          ];
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
            cargoCheckFeatures = [ "tauri/custom-protocol" ];
            nativeCheckInputs = [ pkgs.python3 ];
            preCheck = ''
              (
                cd lycaon-den/src-tauri
                cargo test --locked --offline --quiet --profile release \
                  --features tauri/custom-protocol \
                  --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget} \
                  --package painted-wolf-code --lib \
                  -- --exact ${pkgs.lib.escapeShellArgs desktopTests} \
                  --list --format terse > "$TMPDIR/protected-desktop-tests"
              )
              python3 - "$TMPDIR/protected-desktop-tests" <<'DESKTOP_TESTS'
              from pathlib import Path
              import sys
              path = Path(sys.argv[1])
              if path.stat().st_size > 16384:
                  raise SystemExit('Protected desktop test registry exceeds its bound')
              expected = {name + ': test' for name in ${builtins.toJSON desktopTests}}
              actual = [row for row in path.read_text().splitlines()
                        if row and row != f'{len(expected)} tests, 0 benchmarks']
              if len(actual) != len(expected) or set(actual) != expected:
                  raise SystemExit('Protected desktop test identities differ')
              print('All nine fixed protected desktop tests are registered')
              DESKTOP_TESTS
            '';
            cargoTestFlags = [ "--package" "painted-wolf-code" "--lib" ];
            checkFlags = [ "--exact" ] ++ desktopTests;
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

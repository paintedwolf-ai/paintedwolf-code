{ application, nixpkgs, descriptor }:
  let
    systems = [ "x86_64-linux" "aarch64-linux" ];
    factoryNixpkgs = nixpkgs // {
      lib = nixpkgs.lib // {
        nixosSystem = arguments: nixpkgs.lib.nixosSystem (arguments // {
          modules = arguments.modules ++ [ {
            environment.etc."paintedwolf-factory.json".text = builtins.toJSON {
              schema_version = 1;
              channel = descriptor.channel;
              nixpkgs_revision = nixpkgs.rev;
              application_commit = descriptor.application_commit;
              application_store_path = toString application.packages.${arguments.system}.default;
            };
          } ];
        });
      };
    };
  in {
    packages = nixpkgs.lib.genAttrs systems (system:
      nixpkgs.lib.genAttrs [ "nixos-gnome-vm" "nixos-plasma-vm" ] (component:
        import (application + "/packaging/nix/module-vm.nix") {
          nixpkgs = factoryNixpkgs;
          inherit system;
          package = application.packages.${system}.default;
          desktop = if component == "nixos-gnome-vm" then "gnome" else "plasma";
        }));
  }

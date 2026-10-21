{
  description = "Nonpublishing Painted Wolf Code NixOS nixos-26.05 qualification images";
  inputs.application.url = "git+https://github.com/paintedwolf-ai/paintedwolf-code?ref=refs/qualification/e3e8a59f830243063eba5f488f8dfa9a6199a728&rev=e3e8a59f830243063eba5f488f8dfa9a6199a728";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  outputs = { self, application, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      factoryNixpkgs = nixpkgs // {
        lib = nixpkgs.lib // {
          nixosSystem = arguments: nixpkgs.lib.nixosSystem (arguments // {
            modules = arguments.modules ++ [ {
              environment.etc."paintedwolf-factory.json".text = builtins.toJSON {
                schema_version = 1;
                channel = "nixos-26.05";
                nixpkgs_revision = nixpkgs.rev;
                application_commit = "e3e8a59f830243063eba5f488f8dfa9a6199a728";
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
    };
}

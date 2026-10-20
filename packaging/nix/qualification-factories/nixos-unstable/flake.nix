{
  description = "Nonpublishing Painted Wolf Code NixOS nixos-unstable qualification images";
  inputs.application.url = "git+https://github.com/paintedwolf-ai/paintedwolf-code?ref=refs/qualification/8f1288b52ce5e5853f6dbff5cb4547b5bd25be40&rev=8f1288b52ce5e5853f6dbff5cb4547b5bd25be40";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, application, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      factoryNixpkgs = nixpkgs // {
        lib = nixpkgs.lib // {
          nixosSystem = arguments: nixpkgs.lib.nixosSystem (arguments // {
            modules = arguments.modules ++ [ {
              environment.etc."paintedwolf-factory.json".text = builtins.toJSON {
                schema_version = 1;
                channel = "nixos-unstable";
                nixpkgs_revision = nixpkgs.rev;
                application_commit = "8f1288b52ce5e5853f6dbff5cb4547b5bd25be40";
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

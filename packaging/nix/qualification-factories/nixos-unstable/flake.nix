{
  description = "Nonpublishing Painted Wolf Code NixOS nixos-unstable qualification images";
  inputs.application.url = "git+https://github.com/paintedwolf-ai/paintedwolf-code?ref=refs/qualification/1c1ed7fde5f1c800d6e3fa6a28c72b8791a68af8&rev=1c1ed7fde5f1c800d6e3fa6a28c72b8791a68af8";
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
                application_commit = "1c1ed7fde5f1c800d6e3fa6a28c72b8791a68af8";
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

{
  description = "Nonpublishing Painted Wolf Code NixOS nixos-unstable qualification images";
  inputs.application.url = "git+https://github.com/paintedwolf-ai/paintedwolf-code?ref=refs/qualification/e3e8a59f830243063eba5f488f8dfa9a6199a728&rev=e3e8a59f830243063eba5f488f8dfa9a6199a728";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, application, nixpkgs }:
    import ../factory.nix {
      inherit application nixpkgs;
      descriptor = builtins.fromJSON (builtins.readFile ./factory-inputs.json);
    };
}

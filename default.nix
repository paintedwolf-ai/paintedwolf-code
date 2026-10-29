# Non-flake entry point for the locked package graph in flake.nix.
#
#   nix-build                                   # application for this system
#   pkgs.callPackage painted-wolf-code-src { }  # the same pinned application
#
# The consumer's package set selects only the platform. The release keeps its
# locked nixpkgs, toolchains and converters, so both entry points evaluate to
# the same derivation.
{ stdenv ? null }:
let
  system = if stdenv == null then builtins.currentSystem else stdenv.hostPlatform.system;
  flake = import ./packaging/nix/flake-compat.nix { inherit system; };
  packages = flake.packages.${system}
    or (throw "Painted Wolf Code supports x86_64-linux and aarch64-linux, not ${system}");
in
packages.default // {
  inherit packages;
  inherit (flake) nixosModules;
}

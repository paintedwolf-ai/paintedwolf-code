# Pinned producers embed these dependencies in scripts, flags, or patches rather
# than input lists. Keys are exact derivations, never store-name guesses.
{ pkgs, source }:
let
  inherit (pkgs) lib;
  nixComponents = pkgs.nixVersions.nixComponents_2_34;
  tools = pkgs.callPackage ./toolchains.nix { inherit source; };
in
assert nixComponents.version == pkgs.nix.version;
[
  { package = pkgs.nix;
    inputs = [ pkgs.nix.nix-cli nixComponents.nix-nswrapper ] ++ builtins.attrValues pkgs.nix.libs;
  }
  { package = nixComponents.nix-store; inputs = [ pkgs.busybox-sandbox-shell ]; }
  # glibc embeds this bootstrap runtime in user-defined-trusted-dirs.
  { package = pkgs.glibc; inputs = lib.toList pkgs.glibc.libgcc; }
  { package = pkgs.fontconfig; inputs = [ pkgs.dejavu_fonts.minimal ]; }
  { package = pkgs.webkitgtk_4_1; inputs = [ pkgs.bubblewrap pkgs.xdg-dbus-proxy ]; }
  { package = pkgs.libpciaccess; inputs = [ pkgs.hwdata ]; }
  { package = pkgs.alsa-lib; inputs = [ pkgs.alsa-ucm-conf pkgs.alsa-topology-conf ]; }
  { package = pkgs.gnutls; inputs = [ pkgs.dns-root-data ]; }
  { package = tools.go; inputs = [ pkgs.iana-etc pkgs.mailcap ]; }
]

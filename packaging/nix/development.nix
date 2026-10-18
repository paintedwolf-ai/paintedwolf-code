{ lib, mkShell, rust-bin, go_1_26, bun, nodejs_26, go-task, pkg-config
, python3, git, openssl, gtk3, webkitgtk_4_1, libsoup_3, librsvg
, gsettings-desktop-schemas, glib-networking, gobject-introspection
, source
}:
let
  rust = rust-bin.fromRustupToolchainFile (source + "/rust-toolchain.toml");
  bunVersion = lib.strings.trim (builtins.readFile (source + "/.bun-version"));
in
assert lib.assertMsg (bun.version == bunVersion)
  "The pinned nixpkgs Bun must match .bun-version; update the lock or package override";
mkShell {
  packages = [
    rust go_1_26 bun nodejs_26 go-task pkg-config python3 git
    gobject-introspection
  ];
  buildInputs = [
    openssl gtk3 webkitgtk_4_1 libsoup_3 librsvg
    gsettings-desktop-schemas glib-networking
  ];
  GOTOOLCHAIN = "local";
}

{ mkShell, callPackage, go-task, pkg-config
, python3, git, openssl, gtk3, webkitgtk_4_1, libsoup_3, librsvg
, gsettings-desktop-schemas, glib-networking, gobject-introspection
, source
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
in
mkShell {
  packages = [
    tools.rust tools.go tools.bun tools.nodejs go-task pkg-config python3 git
    gobject-introspection
  ];
  buildInputs = [
    openssl gtk3 webkitgtk_4_1 libsoup_3 librsvg
    gsettings-desktop-schemas glib-networking
  ];
  GOTOOLCHAIN = "local";
}

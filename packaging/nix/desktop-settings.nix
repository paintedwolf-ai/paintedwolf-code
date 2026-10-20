{ lib, makeRustPlatform, callPackage, pkg-config, glib, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  sourcePath = /. + builtins.unsafeDiscardStringContext (toString source);
in rust.buildRustPackage {
  pname = "painted-wolf-code-desktop-settings";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = lib.fileset.toSource {
    root = sourcePath;
    fileset = sourcePath + "/lycaon-den/src-tauri";
  };
  cargoLock.lockFileContents = builtins.readFile (source + "/lycaon-den/src-tauri/Cargo.lock");
  cargoRoot = "lycaon-den/src-tauri";
  buildAndTestSubdir = "lycaon-den/src-tauri";
  cargoBuildFlags = [ "--package" "desktop-settings-observer" "--bin" "pw-desktop-settings" ];
  cargoTestFlags = [ "--package" "desktop-settings-observer" ];
  nativeBuildInputs = [ pkg-config ];
  buildInputs = [ glib ];
  meta = {
    description = "Read-only desktop preferences observer for Painted Wolf Code";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "pw-desktop-settings";
  };
}

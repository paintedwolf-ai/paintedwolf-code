{ lib, makeRustPlatform, callPackage, pkg-config, gtk3, glib, webkitgtk_4_1
, libsoup_3, openssl, source, frontend
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
in rust.buildRustPackage {
  pname = "painted-wolf-code-shell";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = source;
  cargoLock.lockFile = source + "/lycaon-den/src-tauri/Cargo.lock";
  cargoRoot = "lycaon-den/src-tauri";
  buildAndTestSubdir = "lycaon-den/src-tauri";
  cargoBuildFlags = [ "--bin" "painted-wolf-code" ];
  nativeBuildInputs = [ pkg-config ];
  buildInputs = [ gtk3 glib webkitgtk_4_1 libsoup_3 openssl ];
  env.TAURI_CONFIG = builtins.toJSON {
    build.frontendDist = "${frontend}";
    bundle = {
      active = false;
      externalBin = [ ];
      resources = [ ];
      createUpdaterArtifacts = false;
    };
  };
  doCheck = false;
  meta = {
    description = "Native desktop ELF for the Painted Wolf Code Nix application";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "painted-wolf-code";
  };
}

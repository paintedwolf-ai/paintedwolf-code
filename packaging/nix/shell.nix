{ lib, makeRustPlatform, callPackage, pkg-config, gtk3, glib, webkitgtk_4_1
, libsoup_3, openssl, source, frontend
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  sourcePath = /. + builtins.unsafeDiscardStringContext (toString source);
  componentSource = lib.fileset.toSource {
    root = sourcePath;
    fileset = lib.fileset.unions [
      (sourcePath + "/lycaon-den/src-tauri")
      (sourcePath + "/lycaon-den/shared")
      (sourcePath + "/VERSION")
      (sourcePath + "/lycaon/internal/backup/archive-limits.json")
      (sourcePath + "/lycaon/internal/editoroutbox/format.json")
      (sourcePath + "/packaging/update-limits.json")
      (sourcePath + "/packaging/update-keys.json")
      (sourcePath + "/packaging/release-platforms.json")
    ];
  };
in rust.buildRustPackage {
  pname = "painted-wolf-code-shell";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = componentSource;
  cargoLock.lockFileContents = builtins.readFile (source + "/lycaon-den/src-tauri/Cargo.lock");
  cargoRoot = "lycaon-den/src-tauri";
  buildAndTestSubdir = "lycaon-den/src-tauri";
  cargoBuildFlags = [ "--bin" "painted-wolf-code" ];
  # Plain Cargo builds do not receive the production feature from the Tauri CLI.
  cargoBuildFeatures = [ "tauri/custom-protocol" ];
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

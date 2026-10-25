{ lib, callPackage, makeRustPlatform, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  crate = (/. + builtins.unsafeDiscardStringContext (toString source))
    + "/lycaon/internal/documentcore/native";
  componentSource = lib.fileset.toSource { root = crate; fileset = crate; };
in rust.buildRustPackage {
  pname = "painted-wolf-code-document-core";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = componentSource;
  cargoLock.lockFileContents = builtins.readFile (source + "/lycaon/internal/documentcore/native/Cargo.lock");
  cargoBuildFlags = [ "--bin" "pw-document-core" ];
  doCheck = false;
  meta = {
    description = "Painted Wolf Code document synchronization engine";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "pw-document-core";
  };
}

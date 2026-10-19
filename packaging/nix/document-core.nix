{ lib, callPackage, makeRustPlatform, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  crate = (/. + toString source) + "/lycaon/internal/documentcore/native";
in rust.buildRustPackage {
  pname = "painted-wolf-code-document-core";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = lib.fileset.toSource { root = crate; fileset = crate; };
  cargoLock.lockFile = source + "/lycaon/internal/documentcore/native/Cargo.lock";
  cargoBuildFlags = [ "--bin" "pw-document-core" ];
  doCheck = false;
  meta = {
    description = "Painted Wolf Code document synchronization engine";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "pw-document-core";
  };
}

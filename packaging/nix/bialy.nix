{ lib, callPackage, makeRustPlatform, pkg-config, cmake, patchelf, vulkan-loader
, source
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  sourcePath = /. + builtins.unsafeDiscardStringContext (toString source);
  componentSource = lib.fileset.toSource {
    root = sourcePath;
    fileset = lib.fileset.unions [
      (sourcePath + "/lycaon/internal/decide/native")
      (sourcePath + "/scripts/bialy/rank-rubric.json")
    ];
  };
in rust.buildRustPackage {
  pname = "painted-wolf-code-bialy";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = componentSource;
  cargoLock.lockFileContents = builtins.readFile (source + "/lycaon/internal/decide/native/Cargo.lock");
  cargoRoot = "lycaon/internal/decide/native";
  buildAndTestSubdir = "lycaon/internal/decide/native";
  buildFeatures = [ "vulkan" ];
  cargoBuildFlags = [ "--bin" "bialy" ];
  nativeBuildInputs = [ pkg-config cmake patchelf ];
  buildInputs = [ vulkan-loader ];
  # The Vulkan loader is opened by name, so retain its immutable runtime path.
  postFixup = ''
    patchelf --add-rpath ${lib.makeLibraryPath [ vulkan-loader ]} "$out/bin/bialy"
  '';
  doCheck = false;
  meta = {
    description = "Painted Wolf Code local decision engine with Vulkan support";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "bialy";
  };
}

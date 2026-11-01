{ lib, stdenv, callPackage, makeRustPlatform, fetchurl, cmake, ninja, shaderc, vulkan-headers, vulkan-loader
, source
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  pin = (builtins.fromJSON (builtins.readFile (source + "/scripts/generation/sources.json")))."llama.cpp";
  llama = stdenv.mkDerivation {
    pname = "painted-wolf-generation-llama-source";
    version = pin.revision;
    src = fetchurl { inherit (pin) url sha256; };
    meta.license = lib.licenses.mit;
    dontConfigure = true;
    dontBuild = true;
    installPhase = ''mkdir -p "$out"; cp -R . "$out/"'';
  };
  sourcePath = /. + builtins.unsafeDiscardStringContext (toString source);
  componentSource = lib.fileset.toSource {
    root = sourcePath;
    fileset = lib.fileset.unions [
      (sourcePath + "/lycaon/internal/generation")
      (sourcePath + "/lycaon/internal/decide/native/vendor")
      (sourcePath + "/scripts/generation/llama-reference")
    ];
  };
  adapter = stdenv.mkDerivation {
    pname = "painted-wolf-generation-vulkan";
    version = pin.revision;
    src = componentSource;
    nativeBuildInputs = [ cmake ninja shaderc ];
    buildInputs = [ vulkan-headers vulkan-loader ];
    cmakeDir = "scripts/generation/llama-reference";
    cmakeFlags = [
      "-DLLAMA_SOURCE_DIR=${llama}" "-DBUILD_SHARED_LIBS=OFF"
      "-DGGML_VULKAN=ON" "-DGGML_NATIVE=OFF"
      "-DVulkan_GLSLC_EXECUTABLE=${shaderc}/bin/glslc"
    ];
    ninjaFlags = [ "nativegen_vulkan" ];
    installPhase = ''
      mkdir -p "$out/lib"
      install -m644 libnativegen_vulkan.so "$out/lib/"
    '';
    passthru.noticeRuntimeInputs = [ llama ];
    meta.license = lib.licenses.mit;
  };
in rust.buildRustPackage {
  pname = "painted-wolf-code-nativegen";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = componentSource;
  cargoLock.lockFileContents = builtins.readFile (source + "/lycaon/internal/generation/native/Cargo.lock");
  cargoRoot = "lycaon/internal/generation/native";
  buildAndTestSubdir = "lycaon/internal/generation/native";
  buildFeatures = [ "vulkan" ];
  cargoBuildFlags = [ "--bin" "nativegen" ];
  PW_GENERATION_VULKAN_LIB_DIR = "${adapter}/lib";
  buildInputs = [ adapter vulkan-loader ];
  postInstall = ''
    mkdir -p "$out/lib"
    cp ${adapter}/lib/libnativegen_vulkan.so "$out/lib/"
  '';
  doCheck = false;
  passthru = { inherit adapter; };
  meta = {
    description = "Painted Wolf Code E4B generation service with Vulkan support";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "nativegen";
  };
}

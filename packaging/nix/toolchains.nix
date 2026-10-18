{ lib, stdenv, stdenvNoCC, fetchurl, autoPatchelfHook, bun, go_1_26, rust-bin
, source
}:
let
  bunVersion = lib.strings.trim (builtins.readFile (source + "/.bun-version"));
  nodeVersion = lib.strings.trim (builtins.readFile (source + "/.node-version"));
  system = stdenvNoCC.hostPlatform.system;
  bunSources = {
    x86_64-linux = {
      url = "https://registry.npmjs.org/@oven/bun-linux-x64-baseline/-/bun-linux-x64-baseline-1.3.14.tgz";
      hash = "sha512-q/8EdOC0yUE8FPeoOVq8/Pw5I9/tJaYmUfO/uDUAREx8IUnOJH1RJ5A3BjFqre8pvJoiZA9AovPJq5FnNNjSxA==";
    };
    aarch64-linux = {
      url = "https://registry.npmjs.org/@oven/bun-linux-aarch64/-/bun-linux-aarch64-1.3.14.tgz";
      hash = "sha512-X5SsPZHs+iYO8R/efIcRtc7gT2Q2DgPfliCxEkx4cXBumwkw0c/EsHMNwH3EgGpCDaZ7IYVPhpCG/xBOQHEwZw==";
    };
  };
  nodeSources = {
    x86_64-linux = {
      url = "https://nodejs.org/dist/v26.3.0/node-v26.3.0-linux-x64.tar.xz";
      sha256 = "0ce210831380ab75a5fec0d3fb6b5af0958898016b2cae51cc2fa11057d2f77e";
    };
    aarch64-linux = {
      url = "https://nodejs.org/dist/v26.3.0/node-v26.3.0-linux-arm64.tar.xz";
      sha256 = "392f291d00c74644f35ec0325cd03d9d4f5a627c926213fbd05167c0cba9f977";
    };
  };
in
assert lib.assertMsg (bunVersion == "1.3.14" && nodeVersion == "26.3.0")
  "Update the hash-checked Nix toolchain inputs when repository pins change";
assert lib.assertMsg (lib.hasInfix "\ngo 1.26.9\n" (builtins.readFile (source + "/lycaon/go.mod")))
  "Update the Nix Go source hash when go.mod changes";
{
  rust = rust-bin.fromRustupToolchainFile (source + "/rust-toolchain.toml");
  go = go_1_26.overrideAttrs {
    version = "1.26.9";
    src = fetchurl {
      url = "https://go.dev/dl/go1.26.9.src.tar.gz";
      sha256 = "9735d7dcdb65b35d3fa577f04064737c03b89cf1a2b71e6e69fe2f3c6f9fd4ca";
    };
  };
  bun = bun.overrideAttrs {
    version = bunVersion;
    src = fetchurl bunSources.${system};
    sourceRoot = "package/bin";
  };
  nodejs = stdenvNoCC.mkDerivation {
    pname = "nodejs";
    version = nodeVersion;
    src = fetchurl nodeSources.${system};
    nativeBuildInputs = [ autoPatchelfHook ];
    buildInputs = [ stdenv.cc.cc.lib ];
    dontConfigure = true;
    dontBuild = true;
    installPhase = ''
      runHook preInstall
      mkdir -p "$out"
      cp -r bin include lib share "$out/"
      runHook postInstall
    '';
    meta = {
      license = lib.licenses.mit;
      platforms = builtins.attrNames nodeSources;
      mainProgram = "node";
      sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    };
  };
}

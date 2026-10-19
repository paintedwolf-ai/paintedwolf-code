{ lib, stdenv, callPackage, autoPatchelfHook, python3, bun2nix, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  sourcePath = /. + builtins.unsafeDiscardStringContext (toString source);
  componentSource = lib.fileset.toSource {
    root = sourcePath;
    fileset = lib.fileset.unions [
      (lib.fileset.difference (sourcePath + "/lycaon-den") (sourcePath + "/lycaon-den/src-tauri"))
      (sourcePath + "/lycaon-den/src-tauri/tauri.conf.json")
      (sourcePath + "/CHANGELOG.md")
    ];
  };
  dependencyExpression = import ./bun.nix;
  package = lib.importJSON (source + "/lycaon-den/package.json");
  identity = lib.importJSON ./bun-lock.json;
  patchFiles = lib.mapAttrs (_: path: sourcePath + "/lycaon-den/${path}")
    package.patchedDependencies;
in
assert lib.assertMsg
  (builtins.hashFile "sha256" (source + "/lycaon-den/bun.lock") == identity.source_sha256
   && builtins.hashFile "sha256" ./bun.nix == identity.expression_sha256)
  "Regenerate and review the Nix Bun dependency expression after changing bun.lock";
stdenv.mkDerivation {
  pname = "painted-wolf-code-frontend";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = componentSource;
  nativeBuildInputs = [ tools.bun tools.nodejs bun2nix.hook autoPatchelfHook python3 ];
  buildInputs = [ stdenv.cc.cc.lib ];
  bunRoot = "lycaon-den";
  bunDeps = bun2nix.fetchBunDeps {
    bunNix = dependencyExpression;
    patchShebangs = false;
    overrides = bun2nix.patchedDependenciesToOverrides { patchedDependencies = patchFiles; };
  };
  bunInstallFlags = [ "--frozen-lockfile" "--linker=hoisted" "--backend=copyfile" ];
  dontRunLifecycleScripts = true;
  dontUseBunCheck = true;
  dontConfigure = true;
  buildPhase = ''
    runHook preBuild
    test "$(bun --version)" = "${tools.bun.version}"
    test "$(node --version)" = "v${tools.nodejs.version}"
    chmod -R u+w lycaon-den/node_modules
    patchShebangs lycaon-den/node_modules
    python3 ${./native-modules.py} lycaon-den/node_modules ${stdenv.hostPlatform.parsed.cpu.name} > native-modules.list
    while IFS= read -r -d "" native; do
      chmod u+w "$native"
      autoPatchelf "$native"
    done < native-modules.list
    cd lycaon-den
    bun run build
    cd ..
    runHook postBuild
  '';
  installPhase = ''
    runHook preInstall
    mkdir -p "$out"
    cp -r lycaon-den/dist/. "$out/"
    runHook postInstall
  '';
  meta = {
    description = "Locked frontend assets for Painted Wolf Code";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

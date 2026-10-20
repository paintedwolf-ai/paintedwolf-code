{ lib, stdenv, stdenvNoCC, callPackage, patchelf, jq, source
, inputs ? callPackage ./resource-inputs.nix { inherit source; }
, scannerTools ? callPackage ./scanner-tools.nix { inherit source; }
}:
let
  pins = inputs.pins;
  sourceCommit = source.rev or (throw "Scanner consumer qualification requires a committed source revision");
  interpreter = stdenv.cc.bintools.dynamicLinker;
  runtimePath = lib.makeLibraryPath [ stdenv.cc.libc stdenv.cc.cc.lib ];
in stdenvNoCC.mkDerivation {
  pname = "painted-wolf-code-scanner-finalized";
  version = pins.scannerVersion;
  dontUnpack = true;
  dontFixup = true;
  nativeBuildInputs = [ patchelf jq ];
  installPhase = ''
    runHook preInstall
    export HOME="$TMPDIR/scanner-home"
    export XDG_CONFIG_HOME="$HOME/.config" XDG_DATA_HOME="$HOME/.local/share"
    export XDG_STATE_HOME="$HOME/.local/state" XDG_CACHE_HOME="$HOME/.cache"
    mkdir -m 700 -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" "$XDG_CACHE_HOME"
    root="$out/engine-root"
    artifact="$root/bundled/opengrep-${pins.scannerVersion}"
    mkdir -p "$(dirname "$artifact")"
    cp -R ${inputs.scannerOriginal}/artifact "$artifact"
    chmod -R u+w "$artifact"
    arguments=(--set-interpreter ${interpreter} --set-rpath ${runtimePath})
    tool_sha256="$(sha256sum ${patchelf}/bin/patchelf | cut -d' ' -f1)"
    jq -n --arg source_commit ${sourceCommit} \
      --arg flake_lock_sha256 ${builtins.hashFile "sha256" (source + "/flake.lock")} \
      --arg tool_path ${patchelf}/bin/patchelf --arg tool_sha256 "$tool_sha256" \
      --args '{schema_version:1,kind:"nix-nuitka-relocation",source_commit:$source_commit,
        flake_lock_sha256:$flake_lock_sha256,tool_path:$tool_path,tool_sha256:$tool_sha256,
        arguments:$ARGS.positional}' -- "''${arguments[@]}" > recipe.json
    ${scannerTools}/bin/opengrep-artifact -mode relocate-consumer \
      -artifact-directory ${inputs.scannerOriginal}/artifact -root "$root" \
      -consumer-recipe "$PWD/recipe.json" \
      -evidence-root ${source}/lycaon/config/runtime/scanners \
      -audit-output "$out/scanner-audit.json" > "$out/scanner-identity.base64"
    test -s "$out/scanner-identity.base64"
    test -s "$artifact/consumer-finalization.json"
    ${scannerTools}/bin/opengrep-artifact -mode audit \
      -artifact-directory ${inputs.scannerOriginal}/artifact -packaged-directory "$artifact" \
      -consumer-identity-file "$out/scanner-identity.base64" \
      -evidence-root ${source}/lycaon/config/runtime/scanners \
      -audit-output "$out/scanner-audit.json" > /dev/null
    runHook postInstall
  '';
  passthru.original = inputs.scannerOriginal;
  meta = {
    description = "Painted Wolf Code scanner with original producer proof and qualified Nix relocation";
    license = lib.licenses.lgpl21;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

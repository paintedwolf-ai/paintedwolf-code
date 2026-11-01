{ lib, stdenvNoCC, callPackage, source, frontend
, generationRelease ? null
, inputs ? callPackage ./resource-inputs.nix { inherit source; }
, scanner ? callPackage ./scanner.nix { inherit source inputs; }
, notices ? callPackage ./notices.nix { inherit source frontend inputs; }
}:
let
  generationSeed = if generationRelease == null then null
    else callPackage ./generation-seed.nix ({ inherit source; } // generationRelease);
in stdenvNoCC.mkDerivation {
  pname = "painted-wolf-code-resources";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  dontUnpack = true;
  dontFixup = true;
  installPhase = ''
    runHook preInstall
    mkdir -p "$out/engine-root"
    cp -R ${source}/schemas "$out/engine-root/schemas"
    cp -R ${inputs.browser}/browser "$out/engine-root/browser"
    cp -R ${inputs.gitengine}/gitengine "$out/engine-root/gitengine"
    cp -R ${inputs.ocr}/ocr "$out/engine-root/ocr"
    cp -R ${inputs.decision}/decide "$out/engine-root/decide"
    ${lib.optionalString (generationSeed != null) ''
      cp -R ${generationSeed}/engine-root/generation "$out/engine-root/generation"
    ''}
    cp -R ${scanner}/engine-root/bundled "$out/engine-root/bundled"
    cp ${scanner}/scanner-identity.base64 ${scanner}/scanner-audit.json "$out/"
    cp ${notices}/THIRD-PARTY-NOTICES.md "$out/"
    test -s "$out/THIRD-PARTY-NOTICES.md"
    runHook postInstall
  '';
  passthru = {
    inherit inputs scanner notices generationSeed;
    noticeRuntimeInputs = [ inputs.browser inputs.gitengine inputs.ocr inputs.decision scanner notices ]
      ++ lib.optional (generationSeed != null) generationSeed;
  };
  meta = {
    description = "Immutable complete runtime resources for Painted Wolf Code";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

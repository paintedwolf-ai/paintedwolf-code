{ lib, stdenvNoCC, callPackage, source, frontend
, inputs ? callPackage ./resource-inputs.nix { inherit source; }
, scanner ? callPackage ./scanner.nix { inherit source inputs; }
, notices ? callPackage ./notices.nix { inherit source frontend inputs; }
}:
stdenvNoCC.mkDerivation {
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
    cp -R ${scanner}/engine-root/bundled "$out/engine-root/bundled"
    cp ${scanner}/scanner-identity.base64 ${scanner}/scanner-audit.json "$out/"
    cp ${notices}/THIRD-PARTY-NOTICES.md "$out/"
    test -s "$out/THIRD-PARTY-NOTICES.md"
    runHook postInstall
  '';
  passthru = { inherit inputs scanner notices; };
  meta = {
    description = "Immutable complete runtime resources for Painted Wolf Code";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

{ lib, stdenvNoCC, callPackage, source, manifest, authority, payloads, license
, verifier ? callPackage ./model-bundle.nix { inherit source; }
}:
let
  names = builtins.attrNames payloads;
  raw = builtins.readFile manifest;
  signed = builtins.fromJSON raw;
  declared = map (entry: entry.name) ([ signed.manifest.artifact ] ++ (signed.manifest.files or [ ]));
  validName = name: builtins.match "[A-Za-z0-9][A-Za-z0-9._-]{0,127}" name != null
    && name != "manifest.json";
in
assert lib.assertMsg (names != [ ] && builtins.length names <= 65 && lib.all validName names)
  "Generation seeds require explicit, unique payload filenames";
assert lib.assertMsg (builtins.stringLength raw <= 1048576
  && builtins.sort builtins.lessThan declared == names)
  "Generation seed inputs must exactly match the signed manifest";
stdenvNoCC.mkDerivation {
  pname = "painted-wolf-code-generation-seed";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  dontUnpack = true;
  dontFixup = true;
  installPhase = ''
    runHook preInstall
    mkdir -p "$out/engine-root/generation/models/vulkan"
    bundle="$out/engine-root/generation/models/vulkan"
    install -m644 ${lib.escapeShellArg (toString manifest)} "$bundle/manifest.json"
    install -m644 ${lib.escapeShellArg (toString authority)} "$out/engine-root/generation/release-keys.json"
    ${lib.concatStringsSep "\n" (lib.mapAttrsToList (name: file:
      ''install -m644 ${lib.escapeShellArg (toString file)} "$bundle/"${lib.escapeShellArg name}'') payloads)}
    ${verifier}/bin/model-bundle -bundle "$bundle" \
      -authority "$out/engine-root/generation/release-keys.json" \
      -model gemma4-e4b-paintedwolf -device vulkan > "$out/verified-manifest.json"
    runHook postInstall
  '';
  meta = {
    description = "Authenticated Painted Wolf Code Vulkan model seed";
    inherit license;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

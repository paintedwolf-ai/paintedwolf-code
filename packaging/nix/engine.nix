{ lib, buildGoModule, callPackage, binutils, source
, scannerBuildIdentityBase64 ? ""
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  unbound = scannerBuildIdentityBase64 == "";
in
assert lib.assertMsg
  (builtins.pathExists (source + "/third_party/gotreesitter/go.mod")
   && lib.hasInfix "replace github.com/odvcencio/gotreesitter => ../third_party/gotreesitter"
     (builtins.readFile (source + "/lycaon/go.mod")))
  "Keep the reviewed local gotreesitter replacement in the complete source tree";
(buildGoModule.override { go = tools.go; }) {
  pname = "painted-wolf-code-engine" + lib.optionalString unbound "-unbound";
  inherit version;
  src = source;
  modRoot = "lycaon";
  vendorHash = lib.fakeHash;
  subPackages = [ "cmd/lycaon" "cmd/pw-logs" "cmd/pw-vault-identity" ];
  env.CGO_ENABLED = "0";
  tags = [ "paintedwolf_release" ];
  ldflags = [
    "-s" "-w"
    "-X github.com/lycaon/lycaon/internal/version.Version=${version}"
    "-X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${scannerBuildIdentityBase64}"
  ];
  nativeBuildInputs = [ binutils ];
  doCheck = false;
  postInstall = ''
    mv "$out/bin/lycaon" "$out/bin/pw"
    for executable in pw pw-logs pw-vault-identity; do
      if readelf -l "$out/bin/$executable" | grep -Eq 'INTERP|Requesting program interpreter'; then
        echo "Refusing a dynamically linked vault-capable engine: $executable" >&2
        exit 1
      fi
    done
  '';
  passthru.scannerIdentitySupplied = !unbound;
  meta = {
    description = if unbound
      then "Unbound intermediate Painted Wolf Code engine component; requires verified scanner identity for a final package"
      else "Static Painted Wolf Code engine component with an explicitly supplied scanner identity";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "pw";
  };
}

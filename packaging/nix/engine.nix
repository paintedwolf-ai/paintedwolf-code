{ lib, buildGoModule, callPackage, binutils, source
, scannerBuildIdentityBase64 ? ""
, scannerIdentityFile ? null
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  unbound = scannerBuildIdentityBase64 == "" && scannerIdentityFile == null;
in
assert lib.assertMsg (scannerBuildIdentityBase64 == "" || scannerIdentityFile == null)
  "Supply one scanner identity source, never both a literal and a finalized identity file";
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
  vendorHash = "sha256-CiQwCiZ+2Fp67G0Fnjbs2hiPrDeTOwz3RIjkPWpBEFc=";
  subPackages = [ "cmd/lycaon" "cmd/pw-logs" "cmd/pw-vault-identity" ];
  env.CGO_ENABLED = "0";
  tags = [ "paintedwolf_release" ];
  ldflags = [
    "-s" "-w"
    "-X github.com/lycaon/lycaon/internal/version.Version=${version}"
  ] ++ lib.optional (scannerBuildIdentityBase64 != "")
    "-X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${scannerBuildIdentityBase64}";
  preBuild = lib.optionalString (scannerIdentityFile != null) ''
    test -f ${lib.escapeShellArg (toString scannerIdentityFile)}
    identity_bytes="$(stat -c %s ${lib.escapeShellArg (toString scannerIdentityFile)})"
    if test "$identity_bytes" -eq 0 || test "$identity_bytes" -gt 32768; then
      echo "Finalized scanner identity is empty or exceeds its bound" >&2; exit 1
    fi
    scanner_identity="$(cat ${lib.escapeShellArg (toString scannerIdentityFile)})"
    case "$scanner_identity" in
      ""|*[!A-Za-z0-9+/=]*) echo "Finalized scanner identity must be one base64 value" >&2; exit 1 ;;
    esac
    printf '%s' "$scanner_identity" | base64 --decode > /dev/null
    concatTo scanner_ldflags ldflags
    ldflags=("''${scanner_ldflags[@]}" "-X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=$scanner_identity")
  '';
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

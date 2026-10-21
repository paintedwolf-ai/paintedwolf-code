{ lib, callPackage, buildGoModule, python3, apparmor-parser, apparmor-profiles, source }:
let tools = callPackage ./toolchains.nix { inherit source; };
in (buildGoModule.override { go = tools.go; }) {
  pname = "painted-wolf-code-scanner-build-tools";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = source;
  modRoot = "lycaon";
  vendorHash = "sha256-CiQwCiZ+2Fp67G0Fnjbs2hiPrDeTOwz3RIjkPWpBEFc=";
  subPackages = [ "cmd/opengrep-artifact" ];
  env.CGO_ENABLED = "0";
  tags = [ "paintedwolf_release" ];
  ldflags = [ "-s" "-w" ];
  doCheck = false;
  nativeBuildInputs = [ python3 apparmor-parser ];
  # The finalized-scanner build confines the scanner it audits, so a host that
  # restricts user namespaces needs this exact builder's grant before a source build.
  postInstall = ''
    profile=$(cd ${source} && python3 -m scripts.linux_release.nix_apparmor \
      scanner-builder "$out/bin/opengrep-artifact" "$out")
    apparmor_parser --skip-kernel-load --skip-cache --skip-read-cache \
      -I ${apparmor-profiles}/etc/apparmor.d "$profile"
  '';
  meta = {
    description = "Offline Painted Wolf Code scanner admission and consumer qualification tools";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

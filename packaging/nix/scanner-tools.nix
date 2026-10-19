{ lib, callPackage, buildGoModule, source }:
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
  meta = {
    description = "Offline Painted Wolf Code scanner admission and consumer qualification tools";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}

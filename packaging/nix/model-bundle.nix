{ lib, callPackage, buildGoModule, source }:
let tools = callPackage ./toolchains.nix { inherit source; };
in (buildGoModule.override { go = tools.go; }) {
  pname = "painted-wolf-model-bundle";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = source;
  modRoot = "lycaon";
  vendorHash = import ./go-vendor-hash.nix;
  subPackages = [ "cmd/model-bundle" ];
  env.CGO_ENABLED = "0";
  ldflags = [ "-s" "-w" ];
  doCheck = false;
  meta = {
    description = "Painted Wolf Code model export authentication tool";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "model-bundle";
  };
}

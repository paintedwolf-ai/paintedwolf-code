# Evaluate the real pinned forwarding wrapper through the production descriptor.
{ pkgs }:
let
  describe = import ./closure-output.nix { inherit (pkgs) lib; };
  package = pkgs.libidn2;
  original = package.out;
  out = describe package "out";
  bin = describe package "bin";
  dev = describe package "dev";
  path = value: builtins.unsafeDiscardStringContext value.outPath;
in
assert out.outputs == [ (path original) ];
assert out.source != null;
assert out.source.store_path == path original.src;
assert out.source.output_hash == original.src.outputHash;
assert out.source.output_hash != null;
assert bin.outputs == [ (path package.bin) ];
assert dev.outputs == [ (path package.dev) ];
assert bin.source == null && dev.source == null;
assert out.outputs != bin.outputs && out.outputs != dev.outputs;
true

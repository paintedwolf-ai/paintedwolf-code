# Evaluate the real pinned forwarding wrapper through the production descriptor.
{ pkgs, roots, runtimeEdges, nixpkgsRevision }:
let
  inherit (pkgs) lib;
  catalogue = import ./closure-catalogue.nix { inherit lib; };
  packages = catalogue { inherit roots runtimeEdges; };
  find = packages: drv: lib.findFirst
    (entry: entry.outputs == [ (path drv) ]) null packages;
  regression = builtins.fromJSON (builtins.readFile ./closure-notices-regression.json);
  expectedPaths = regression.${pkgs.stdenv.hostPlatform.system} or [ ];
  describedPaths = lib.concatMap (entry: entry.outputs) packages;
  missing = lib.filter (expected: !(lib.elem expected describedPaths)) expectedPaths;
  nix = find packages pkgs.nix;
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
# Recorded real-closure misses are pin-specific identities, never inferred owners.
assert regression.nixpkgs_revision == nixpkgsRevision;
assert lib.assertMsg (missing == [ ])
  ("Pinned closure outputs lack catalogue metadata: " + lib.concatStringsSep ", " missing);
assert lib.all (edge: lib.all (drv: find packages drv != null) edge.inputs) runtimeEdges;
assert find packages pkgs.nix.nix-cli != null;
assert find packages pkgs.mesa != null;
assert nix != null && nix.source != null;
assert nix.source.store_path == path pkgs.nix.src;
assert nix.source.output_hash == pkgs.nix.src.outputHash;
assert nix.source.output_hash != null;
true

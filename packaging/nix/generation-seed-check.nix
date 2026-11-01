{ lib, callPackage, source }:
let
  fixture = source + "/packaging/nix/fixtures/generation-seed";
  inputs = {
    inherit source;
    manifest = fixture + "/manifest.json";
    authority = fixture + "/authority.json";
    payloads."weights.fixture" = fixture + "/weights.fixture";
    license = lib.licenses.asl20;
  };
  seed = values: callPackage ./generation-seed.nix values;
  rejected = payloads: !(builtins.tryEval ((seed (inputs // { inherit payloads; })).drvPath)).success;
in
assert rejected { };
assert rejected { "../weights" = fixture + "/weights.fixture"; };
assert rejected (inputs.payloads // { "unlisted" = fixture + "/weights.fixture"; });
assert rejected { "manifest.json" = fixture + "/manifest.json"; };
seed inputs

# Output provenance belongs to the selected derivation, including forwarded outputs.
{ lib, runtimeEdges ? [ ] }:
let
  licence = value: {
    spdx = value.spdxId or null;
    name = value.fullName or value.shortName or (if builtins.isString value then value else null);
    free = value.free or null;
    redistributable = value.redistributable or null;
  };
  sourceOf = drv: let src = (drv.drvAttrs or drv).src or (drv.src or null); in
    if src == null || !(lib.isDerivation src) then [ ] else [ {
      store_path = builtins.unsafeDiscardStringContext src.outPath;
      urls = src.urls or (lib.optional (src ? url) src.url);
      output_hash = src.outputHash or null;
    } ];
in
package: output: let drv = package.${output}; meta = drv.meta or { }; in {
    name = drv.pname or drv.name;
    version = drv.version or null;
    outputs = [ (builtins.unsafeDiscardStringContext drv.outPath) ];
    licenses = map licence (lib.toList (meta.license or [ ]));
    homepage = meta.homepage or null;
    sources = sourceOf drv ++ lib.concatMap (edge: edge.sources or [ ])
      (lib.filter (edge: edge.package.drvPath == drv.drvPath) runtimeEdges);
    nixpkgs_position = if meta ? position
      then builtins.unsafeDiscardStringContext (lib.last (lib.splitString "/nix/store/" meta.position)) else null;
  }

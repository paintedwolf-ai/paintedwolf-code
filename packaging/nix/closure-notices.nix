# Licence notices and corresponding-source references for every store path a
# redistributed application closure contains. It is a sibling of the
# application, never part of its closure, and fails when a path has no
# reviewed provenance.
{ lib, runCommand, closureInfo, python3, source, application, components, nixpkgsRevision }:
let
  inputNames = [ "buildInputs" "propagatedBuildInputs" "nativeBuildInputs" "propagatedNativeBuildInputs"
                 "depsBuildBuild" "depsHostHost" "depsTargetTarget" ];
  # Build inputs, the standard environment (its shell backs generated wrappers)
  # and sources; string-interpolated components are named by the caller.
  # Read the derivation's own arguments; package aliases warn on their attributes.
  inputsOf = drv: let attrs = drv.drvAttrs or drv; in lib.filter lib.isDerivation
    (lib.flatten (map (name: attrs.${name} or [ ]) inputNames ++ [ (attrs.src or [ ]) (attrs.stdenv or [ ]) ]));
  reachable = builtins.genericClosure {
    startSet = map (drv: { key = drv.drvPath; inherit drv; }) ([ application ] ++ components);
    operator = item: map (drv: { key = drv.drvPath; inherit drv; }) (inputsOf item.drv);
  };
  licence = value: {
    spdx = value.spdxId or null;
    name = value.fullName or value.shortName or (if builtins.isString value then value else null);
    free = value.free or null;
    redistributable = value.redistributable or null;
  };
  sourceOf = drv: let src = (drv.drvAttrs or drv).src or null; in
    if src == null || !(lib.isDerivation src) then null else {
      store_path = builtins.unsafeDiscardStringContext src.outPath;
      urls = src.urls or (lib.optional (src ? url) src.url);
      output_hash = src.outputHash or null;
    };
  describe = item: let drv = item.drv; meta = drv.meta or { }; in {
    name = drv.pname or drv.name;
    version = drv.version or null;
    outputs = map (output: builtins.unsafeDiscardStringContext drv.${output}.outPath) (drv.outputs or [ "out" ]);
    licenses = map licence (lib.toList (meta.license or [ ]));
    homepage = meta.homepage or null;
    source = sourceOf drv;
    nixpkgs_position = if meta ? position
      then builtins.unsafeDiscardStringContext (lib.last (lib.splitString "/nix/store/" meta.position)) else null;
  };
  catalog = builtins.toFile "painted-wolf-code-closure-catalog.json" (builtins.toJSON {
    format_version = 1;
    nixpkgs_revision = nixpkgsRevision;
    application = builtins.unsafeDiscardStringContext application.outPath;
    source_commit = source.rev or null;
    packages = map describe reachable;
  });
in
runCommand "painted-wolf-code-closure-notices-${application.version}" {
  nativeBuildInputs = [ python3 ];
  closure = closureInfo { rootPaths = [ application ]; };
} ''
  python3 ${source}/scripts/linux_release/nix_closure_notices.py \
    --catalog ${catalog} --store-paths "$closure/store-paths" --out "$out"
''

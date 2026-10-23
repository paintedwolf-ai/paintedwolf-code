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
  describe = import ./closure-output.nix { inherit lib; };
  catalog = builtins.toFile "painted-wolf-code-closure-catalog.json" (builtins.toJSON {
    format_version = 1;
    nixpkgs_revision = nixpkgsRevision;
    application = builtins.unsafeDiscardStringContext application.outPath;
    source_commit = source.rev or null;
    packages = lib.concatMap (item: map (describe item.drv) (item.drv.outputs or [ "out" ])) reachable;
  });
in
runCommand "painted-wolf-code-closure-notices-${application.version}" {
  nativeBuildInputs = [ python3 ];
  closure = closureInfo { rootPaths = [ application ]; };
} ''
  python3 ${source}/scripts/linux_release/nix_closure_notices.py \
    --catalog ${catalog} --store-paths "$closure/store-paths" --out "$out"
''

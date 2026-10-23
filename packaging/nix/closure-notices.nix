# Licence notices and corresponding-source references for every store path a
# redistributed application closure contains. It is a sibling of the
# application, never part of its closure, and fails when a path has no
# reviewed provenance.
{ lib, runCommand, closureInfo, python3, source, application, components, runtimeEdges, nixpkgsRevision }:
let
  catalogue = import ./closure-catalogue.nix { inherit lib; };
  packages = catalogue {
    roots = [ application ] ++ components ++ application.noticeRuntimeInputs;
    inherit runtimeEdges;
  };
  catalog = builtins.toFile "painted-wolf-code-closure-catalog.json" (builtins.toJSON {
    format_version = 1;
    nixpkgs_revision = nixpkgsRevision;
    application = builtins.unsafeDiscardStringContext application.outPath;
    source_commit = source.rev or null;
    inherit packages;
  });
in
runCommand "painted-wolf-code-closure-notices-${application.version}" {
  nativeBuildInputs = [ python3 ];
  closure = closureInfo { rootPaths = [ application ]; };
} ''
  python3 ${source}/scripts/linux_release/nix_closure_notices.py \
    --catalog ${catalog} --store-paths "$closure/store-paths" --out "$out"
''

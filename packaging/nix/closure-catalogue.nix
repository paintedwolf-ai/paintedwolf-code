# Metadata follows selected outputs; runtime edges retain the actual package objects.
{ lib }:
{ roots, runtimeEdges ? [ ] }:
let
  inputNames = [
    "depsBuildBuild" "depsBuildBuildPropagated"
    "nativeBuildInputs" "propagatedNativeBuildInputs"
    "depsBuildTarget" "depsBuildTargetPropagated"
    "depsHostHost" "depsHostHostPropagated"
    "buildInputs" "propagatedBuildInputs"
    "depsTargetTarget" "depsTargetTargetPropagated"
  ];
  selected = package: map (name: package.${name}) (package.outputs or [ "out" ]);
  node = drv: { key = builtins.unsafeDiscardStringContext drv.outPath; inherit drv; };
  inputsOf = drv:
    let
      attrs = drv.drvAttrs or drv;
      embedded = lib.concatMap (edge: edge.inputs)
        (lib.filter (edge: edge.package.drvPath == drv.drvPath) runtimeEdges);
    in lib.filter lib.isDerivation (lib.flatten (
      map (name: attrs.${name} or [ ]) inputNames
      ++ [ (attrs.src or [ ]) (attrs.stdenv or [ ]) (drv.noticeRuntimeInputs or [ ]) embedded ]
    ));
  reachable = builtins.genericClosure {
    startSet = map node (lib.concatMap selected roots);
    operator = item: map node (lib.concatMap selected (inputsOf item.drv));
  };
  describe = import ./closure-output.nix { inherit lib runtimeEdges; };
in
map (item: describe item.drv (item.drv.outputName or "out")) reachable

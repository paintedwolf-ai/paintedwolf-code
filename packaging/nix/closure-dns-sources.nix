# Generated data has several source inputs, including files in the pinned recipe.
{ pkgs, nixpkgs }:
let
  hints = pkgs.fetchurl {
    urls = map (prefix: prefix + "d9c96ae96f066a85d7/etc/root.hints") [
      "https://gitlab.nic.cz/knot/knot-resolver/raw/"
      "https://raw.githubusercontent.com/CZ-NIC/knot-resolver/"
    ];
    hash = "sha256-4lG/uPnNHBNIZ/XIeDM1w3iukrpeW0JIjTnGSwkJ8U4=";
  };
  context = builtins.getContext pkgs.dns-root-data.drvAttrs.buildCommand;
  recipeFile = name: builtins.path {
    path = nixpkgs.outPath + "/pkgs/by-name/dn/dns-root-data/" + name;
    inherit name;
  };
in
assert pkgs.dns-root-data.version == "2025-04-14";
assert builtins.hasAttr hints.drvPath context;
assert builtins.hasAttr (toString (recipeFile "root.key")) context;
assert builtins.hasAttr (toString (recipeFile "root.ds")) context;
[
  { store_path = builtins.unsafeDiscardStringContext nixpkgs.outPath;
    urls = [ "https://github.com/NixOS/nixpkgs/archive/${nixpkgs.rev}.tar.gz" ];
    output_hash = nixpkgs.narHash;
  }
  { store_path = builtins.unsafeDiscardStringContext hints.outPath;
    inherit (hints) urls;
    output_hash = hints.outputHash;
  }
]

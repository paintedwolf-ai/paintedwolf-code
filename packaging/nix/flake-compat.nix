# Evaluates flake.nix with the inputs pinned in flake.lock, without requiring
# the flakes feature. A Git checkout contributes only its tracked files.
{ system ? builtins.currentSystem }:
let
  compat = builtins.fetchTarball {
    url = "https://github.com/NixOS/flake-compat/archive/5edf11c44bc78a0d334f6334cdaf7d60d732daab.tar.gz";
    sha256 = "0yqfa6rx8md81bcn4szfp0hjq2f3h9i8zjzhqqyfqdkrj5559nmw";
  };
in
(import compat { src = ../..; inherit system; }).outputs

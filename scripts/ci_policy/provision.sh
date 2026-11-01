#!/usr/bin/env bash
# Populate pinned verification inputs before the offline lane starts.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
if [[ "${1:-}" == search-reference ]]; then
  [[ $# -eq 2 ]] || { echo "error: expected a search-reference destination" >&2; exit 2; }
  exec python3 "$ROOT/scripts/ci_policy/search_reference.py" "$2"
fi
if [[ "${1:-}" == repository-tools ]]; then
  if [[ $# -eq 3 && "$2" == --signer ]]; then
    exec nix --extra-experimental-features "nix-command flakes" build --inputs-from "$ROOT" --no-write-lock-file nixpkgs#minisign --out-link "$3"
  fi
  if [[ $# -eq 3 && "$2" == --python ]]; then
    exec nix --extra-experimental-features "nix-command flakes" build --inputs-from "$ROOT" --no-write-lock-file --impure \
      --expr 'let pkgs = (builtins.getFlake "nixpkgs").legacyPackages.${builtins.currentSystem}; in pkgs.python3.withPackages (p: [ p.pip ])' \
      --out-link "$3"
  fi
  [[ $# -eq 1 ]] || { echo "error: expected repository-tools [--signer DIRECTORY | --python DIRECTORY]" >&2; exit 2; }
  case "$(uname -s)" in
    Linux)
      sudo apt-get update -qq
      sudo apt-get install -y --no-install-recommends apt-utils createrepo-c gnupg libarchive-tools makepkg minisign pacman-package-manager rpm
      ;;
    Darwin) HOMEBREW_NO_AUTO_UPDATE=1 brew install minisign ;;
    *) echo "error: repository tools require macOS or apt-based Linux" >&2; exit 2 ;;
  esac
  exit 0
fi
case "${1:-verification}" in
  verification|analysis) ;;
  *) echo "error: unknown CI input profile: $1" >&2; exit 2 ;;
esac
[[ $# -le 1 ]] || { echo "error: expected one CI input profile" >&2; exit 2; }
python3 "$ROOT/scripts/ci_policy/go_retry.py" "$ROOT/lycaon"
python3 "$ROOT/scripts/ci_policy/go_retry.py" "$ROOT/scripts/commentlint"
PYTHONPATH="$ROOT/scripts${PYTHONPATH:+:$PYTHONPATH}" python3 -m workflow_lint --provision
[[ "${1:-verification}" != analysis ]] || exit 0
# Lanes cannot reach the module proxy, so pinned tools are built here, not by `go run`.
python3 "$ROOT/scripts/analysis_tools.py" ensure sqlc
# Changed scripts coverage runs its suites under the pinned coverage.py.
python3 "$ROOT/scripts/coverage_changes.py" python --provision
for manifest in "$ROOT/lycaon/internal/documentcore/native/Cargo.toml" "$ROOT/lycaon/internal/decide/native/Cargo.toml"; do
  cargo fetch --locked --manifest-path "$manifest"
done

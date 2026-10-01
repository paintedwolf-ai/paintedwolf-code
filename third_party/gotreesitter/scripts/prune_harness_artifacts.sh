#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/prune_harness_artifacts.sh [--delete] [--delete-receipts] [--include-golden]

Lists repo-local generated artifacts by category and size. The default is a
dry run. Ordinary --delete removes only reproducible caches and root build
artifacts; run receipts require the separate --delete-receipts opt-in.

Options:
  --delete           Remove reproducible caches and root build artifacts.
  --delete-receipts  Also remove local harness/benchmark receipt directories.
  --include-golden   Also include .golden/ generated golden corpus artifacts.
  -h, --help         Show this help.
USAGE
}

delete=0
delete_receipts=0
include_golden=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete)
      delete=1
      ;;
    --delete-receipts)
      delete=1
      delete_receipts=1
      ;;
    --include-golden)
      include_golden=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir/.." rev-parse --show-toplevel 2>/dev/null || (cd "$script_dir/.." && pwd))"
cd "$repo_root"

cache_targets=(
  "cgo_harness/corpus_real"
  "cgo_harness/grammar_seed"
  ".parity_seed"
)

receipt_targets=(
  "harness_out"
  "parity_out"
  "cgo_harness/harness_out"
  "cgo_harness/parity_out"
  "cgo_harness/reports"
  "cgo_harness/bench/runs"
)

if [[ "$include_golden" -eq 1 ]]; then
  cache_targets+=(".golden")
fi

root_artifacts=()
while IFS= read -r -d '' artifact; do
  root_artifacts+=("${artifact#./}")
done < <(
  find . -maxdepth 1 -type f \
    \( -name '*.test' -o -name '*.log' -o -name '*.prof' \) \
    -print0
)

for artifact in scannertest ts2go tsquery parity_report benchgate nul; do
  if [[ -f "$artifact" ]]; then
    root_artifacts+=("$artifact")
  fi
done

found=0

report_group() {
  local label="$1"
  local remove="$2"
  shift 2
  local target size
  local printed_header=0
  for target in "$@"; do
    if [[ ! -e "$target" ]]; then
      continue
    fi
    found=1
    if [[ "$printed_header" -eq 0 ]]; then
      printf '%s:\n' "$label"
      printed_header=1
    fi
    size="$(du -sh -- "$target" | awk '{print $1}')"
    if [[ "$remove" -eq 1 ]]; then
      printf '  removing %8s %s\n' "$size" "$target"
      rm -rf -- "$target"
    else
      printf '  %8s %s\n' "$size" "$target"
    fi
  done
}

report_group "root build artifacts" "$delete" "${root_artifacts[@]}"
report_group "reproducible caches" "$delete" "${cache_targets[@]}"
report_group "run receipts (preserved without --delete-receipts)" "$delete_receipts" "${receipt_targets[@]}"

if [[ "$found" -eq 0 ]]; then
  echo "no generated local artifacts found"
elif [[ "$delete" -eq 0 ]]; then
  echo
  echo "dry run only; --delete removes root artifacts and caches"
  echo "run receipts require the separate --delete-receipts opt-in"
elif [[ "$delete_receipts" -eq 0 ]]; then
  echo
  echo "run receipts preserved; use --delete-receipts only after archiving durable evidence"
fi

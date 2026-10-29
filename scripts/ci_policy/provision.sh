#!/usr/bin/env bash
# Populate pinned compiler/generator inputs before the offline lane starts.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
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

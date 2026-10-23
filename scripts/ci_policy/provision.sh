#!/usr/bin/env bash
# Populate pinned compiler/generator inputs before the offline lane starts.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
(cd "$ROOT/lycaon" && go mod download)
(cd "$ROOT/scripts/commentlint" && go mod download)
# Lanes cannot reach the module proxy, so pinned tools are built here, not by `go run`.
python3 "$ROOT/scripts/analysis_tools.py" ensure sqlc
# Changed scripts coverage runs its suites under the pinned coverage.py.
python3 "$ROOT/scripts/coverage_changes.py" python --provision
for manifest in "$ROOT/lycaon/internal/documentcore/native/Cargo.toml" "$ROOT/lycaon/internal/decide/native/Cargo.toml"; do
  cargo fetch --locked --manifest-path "$manifest"
done

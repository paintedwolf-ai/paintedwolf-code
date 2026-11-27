#!/usr/bin/env bash
# Fails if the fuzz-findings branch has any open finding whose input is not
# committed byte-identically in this tree.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "test-fuzz-findings" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/fuzz-findings.sh "$@"
fi
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
RECEIPT="${PW_FUZZ_FINDINGS_RECEIPT:-${PW_ARTIFACT_ROOT}/ci/fuzz-findings.json}"
PYTHONPATH="${ROOT}/scripts${PYTHONPATH:+:${PYTHONPATH}}" exec python3 -m fuzz_plan.findings check --repo "$ROOT" --receipt "$RECEIPT" "$@"

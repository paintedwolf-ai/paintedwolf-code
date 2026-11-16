#!/usr/bin/env bash
# Explores the plan's fuzz targets: `run [PACKAGE ...]` for their declared time,
# `smoke` briefly in the packages the change touches. See scripts/fuzz_plan.py.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "fuzz-$1" -- bash "$0" "$@"
fi
SOURCE_SNAPSHOT_SH="${ROOT}/scripts/test-source-snapshot.sh"
if ! bash "$SOURCE_SNAPSHOT_SH" holding; then
  exec bash "$SOURCE_SNAPSHOT_SH" run -- bash scripts/fuzz-short.sh "$@"
fi
# shellcheck source=document-core-env.sh
source "${ROOT}/scripts/document-core-env.sh"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
: "${PW_FUZZ_WORKERS:?verification admission must set PW_FUZZ_WORKERS}"
exec python3 "${ROOT}/scripts/fuzz_plan.py" "$@"

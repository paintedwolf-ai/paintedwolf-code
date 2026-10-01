#!/usr/bin/env bash
# Bind verification to the selected engine's authenticated identity.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "with-bundled-scanners" -- bash "$0" "$@"
fi
[[ "${1:-}" == "--" ]] && shift
[[ $# -gt 0 ]] || { echo "usage: with-bundled-scanners.sh -- command [args...]" >&2; exit 2; }
OPENGREP_DIR="$(bash "$ROOT/scripts/resolve-opengrep.sh" --dir-only)"
OPENGREP_IDENTITY="$(bash "$ROOT/scripts/resolve-opengrep.sh" --identity-only)"
export GOFLAGS="${GOFLAGS:-} -ldflags=-X=github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${OPENGREP_IDENTITY}"
export LYCAON_ENGINE_ROOT="$(dirname "$(dirname "$OPENGREP_DIR")")"
export LYCAON_SCANNER_SUITE_REQUIRED=1
exec "$@"

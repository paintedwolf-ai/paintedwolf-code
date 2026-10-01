#!/usr/bin/env bash
# Run govulncheck on the lycaon module with reviewed allowlist enforcement.
# Usage: scripts/lint-vuln.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! bash "$ROOT/scripts/repo-snapshot-lock.sh" holding; then
  exec bash "$ROOT/scripts/repo-snapshot-lock.sh" read -- "$0" "$@"
fi
exec python3 "${ROOT}/scripts/govulncheck-gate.py"

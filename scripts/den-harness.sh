#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" == "--prepare" && "$#" == "1" ]]; then
  : "${LYCAON_E2E_STATE_DIR:?harness preparation requires a state directory}"
  exec bash "${DIR}/e2e-sidecar-build.sh"
fi
if [[ "${1:-}" != "--supervised" ]]; then
  [[ "$#" == "0" ]] || { echo "error: unknown harness arguments: $*" >&2; exit 2; }
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift
# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"

harness_up --interactive
harness_banner

# The supervisor stops the stack when the frontend exits.
bash "${SCRIPTS}/e2e-vite.sh"

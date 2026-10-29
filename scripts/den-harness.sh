#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" == "--prepare" && "$#" == "1" ]]; then
  : "${LYCAON_E2E_STATE_DIR:?harness preparation requires a state directory}"
  exec bash "${DIR}/e2e-sidecar-build.sh"
fi
if [[ "${1:-}" != "--supervised" ]]; then
  if [[ "$#" == "1" && "$1" == "--reuse-built" ]]; then
    [[ -n "${LYCAON_E2E_STATE_DIR:-}" && -x "${LYCAON_E2E_STATE_DIR}/runtime/sidecar" ]] || {
      echo "error: --reuse-built requires LYCAON_E2E_STATE_DIR with a prepared runtime/sidecar" >&2
      exit 2
    }
  elif [[ "$#" != "0" ]]; then
    echo "error: unknown harness arguments: $*" >&2
    exit 2
  fi
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift
# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"

harness_up --interactive "$@"
harness_banner

# The supervisor stops the stack when the frontend exits.
bash "${SCRIPTS}/e2e-vite.sh"

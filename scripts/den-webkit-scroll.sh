#!/usr/bin/env bash
# Probes for WebKit's threaded scrolling, then runs its invariants against the harness Den in a real WKWebView.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" != "--supervised" ]]; then
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift
[[ "$#" == "0" ]] || { echo "error: den:webkit:scroll takes no arguments: $*" >&2; exit 2; }
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "den:webkit:scroll: WebKit's threaded scrolling is macOS only; nothing to check here"
  exit 0
fi
ROOT="$(cd "${DIR}/.." && pwd)"
# Matches webkit-harness's ABSENT_ON_VIRTUAL_MACHINE.
ABSENT_ON_VIRTUAL_MACHINE=3

harness() {
  (cd "${ROOT}/lycaon-den/src-tauri" && cargo run --quiet --locked -p webkit-harness -- "$@")
}

# The probe builds only the harness, so a host without a scrolling thread skips before the stack builds.
status=0
harness probe || status=$?
if ((status == ABSENT_ON_VIRTUAL_MACHINE)); then
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    echo "::notice title=WebKit scroll invariants::Skipped: this virtual machine's WebKit has no scrolling thread"
  fi
  exit 0
fi
((status == 0)) || exit "${status}"

# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"
# The runner scripts every model completion.
export LYCAON_LLM_MANUAL=1
# File watching would reload the page during a run.
export LYCAON_E2E_FROZEN=1

harness_up
harness_vite_bg

harness scroll-invariants "${LYCAON_E2E_BASE_URL}" || status=$?
if ((status != 0)) && [[ -f "${LYCAON_E2E_STATE_DIR}/sidecar.log" ]]; then
  # The harness removes its state directory on exit.
  echo "den:webkit:scroll: sidecar log tail" >&2
  tail -n 80 "${LYCAON_E2E_STATE_DIR}/sidecar.log" >&2
fi
exit "${status}"

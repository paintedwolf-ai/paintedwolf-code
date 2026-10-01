#!/usr/bin/env bash
# Runs the WebKit scroll-thread invariants against the harness Den in a real WKWebView.
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
# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"

# The runner scripts every model completion.
export LYCAON_LLM_MANUAL=1
# File watching would reload the page during a run.
export LYCAON_E2E_FROZEN=1

harness_up
harness_vite_bg

bash "${ROOT}/scripts/ensure-tauri-binaries.sh"
cd "${ROOT}/lycaon-den/src-tauri"
cargo run --quiet --example scroll_thread_invariants -- "${LYCAON_E2E_BASE_URL}"

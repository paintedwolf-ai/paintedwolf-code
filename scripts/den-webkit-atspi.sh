#!/usr/bin/env bash
# Asks WebKitGTK for AT-SPI text attributes across the harness Den's sidebar, as Orca does (Linux).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" != "--supervised" ]]; then
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift
[[ "$#" == "0" ]] || { echo "error: den:webkit:atspi takes no arguments: $*" >&2; exit 2; }
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "den:webkit:atspi: WebKitGTK accessibility is Linux only; nothing to check here"
  exit 0
fi

PACKAGES=(gir1.2-webkit2-4.1 gir1.2-atspi-2.0 python3-gi at-spi2-core xvfb dbus)
probe_ready() {
  /usr/bin/python3 -c 'import gi; gi.require_version("WebKit2", "4.1"); gi.require_version("Atspi", "2.0"); from gi.repository import WebKit2, Atspi' 2>/dev/null &&
    command -v xvfb-run >/dev/null && command -v dbus-run-session >/dev/null
}
if ! probe_ready && [[ "${GITHUB_ACTIONS:-}" == "true" ]] && command -v apt-get >/dev/null; then
  sudo apt-get update -qq
  sudo apt-get install -y --no-install-recommends "${PACKAGES[@]}"
fi
if ! probe_ready; then
  echo "den:webkit:atspi: unverified: needs ${PACKAGES[*]}" >&2
  exit 2
fi

# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"
# File watching would reload the page during a run.
export LYCAON_E2E_FROZEN=1

harness_up
harness_vite_bg

# A private session bus and display keep the probe off the developer's desktop and screen reader.
status=0
dbus-run-session -- bash -c '
  busctl --user set-property org.a11y.Bus /org/a11y/bus org.a11y.Status IsEnabled b true
  exec xvfb-run -a /usr/bin/python3 "$0" "$1"
' "${DIR}/harness/webkit_atspi_text_attributes.py" "${LYCAON_E2E_BASE_URL}" || status=$?
if ((status == 1)) && [[ -f "${LYCAON_E2E_STATE_DIR}/sidecar.log" ]]; then
  echo "den:webkit:atspi: sidecar log tail" >&2
  tail -n 40 "${LYCAON_E2E_STATE_DIR}/sidecar.log" >&2
fi
exit "${status}"

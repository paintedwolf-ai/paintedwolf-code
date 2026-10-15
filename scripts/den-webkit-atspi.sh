#!/usr/bin/env bash
# Asks each supported WebKitGTK release for AT-SPI text attributes across the harness Den, as Orca does (Linux).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" != "--supervised" ]]; then
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift

# WebKitGTK release → container image that ships it, and its package install.
declare -A IMAGE=(
  [2.50]="ubuntu:22.04"
  [2.52]="ubuntu:24.04"
  [2.54]="fedora:44"
)
APT="apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends gir1.2-webkit2-4.1 gir1.2-atspi-2.0 python3-gi at-spi2-core xvfb xauth dbus >/dev/null && rm -rf /var/lib/apt/lists/*"
declare -A INSTALL=(
  [2.50]="${APT}"
  [2.52]="${APT}"
  [2.54]="dnf -y -q install webkit2gtk4.1 gtk3 python3-gobject gobject-introspection at-spi2-core xorg-x11-server-Xvfb xorg-x11-xauth util-linux dbus-daemon dbus-tools mesa-dri-drivers >/dev/null && dnf clean all"
)
ENGINES=(2.50 2.52 2.54)

if (($#)); then
  for engine in "$@"; do
    [[ -n "${IMAGE[${engine}]:-}" ]] || { echo "error: unknown WebKitGTK release ${engine}; known: ${ENGINES[*]}" >&2; exit 2; }
  done
  ENGINES=("$@")
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "den:webkit:atspi: unverified: WebKitGTK accessibility runs on Linux only" >&2
  exit 2
fi
RUNTIME=""
for candidate in docker podman; do
  if command -v "${candidate}" >/dev/null && "${candidate}" info >/dev/null 2>&1; then
    RUNTIME="${candidate}"
    break
  fi
done
if [[ -z "${RUNTIME}" ]]; then
  echo "den:webkit:atspi: unverified: needs a working docker or podman to run each WebKitGTK release" >&2
  exit 2
fi

# shellcheck source=scripts/harness/lib.sh
source "${DIR}/harness/lib.sh"
# File watching would reload the page during a run.
export LYCAON_E2E_FROZEN=1

# A harness that cannot start leaves the engines unchecked.
trap 'echo "den:webkit:atspi: unverified: the harness Den did not start" >&2; exit 2' ERR
harness_up
harness_vite_bg
trap - ERR

# The probe runs on a private session bus, accessibility bus and display inside the container.
read -r -d '' SESSION <<'EOF' || true
launcher="$(ls /usr/libexec/at-spi-bus-launcher /usr/lib/at-spi2-core/at-spi-bus-launcher 2>/dev/null | head -n 1)"
[ -n "${launcher}" ] || { echo "unverified: at-spi-bus-launcher not found" >&2; exit 2; }
installed="$(python3 -c 'import gi; gi.require_version("WebKit2", "4.1"); from gi.repository import WebKit2; print(f"{WebKit2.get_major_version()}.{WebKit2.get_minor_version()}.{WebKit2.get_micro_version()}")')"
case "${installed}" in
  "${EXPECTED}".*) echo "WebKitGTK ${installed}" ;;
  *) echo "unverified: the image ships WebKitGTK ${installed}, not ${EXPECTED}; update the release table" >&2; exit 2 ;;
esac
exec dbus-run-session -- sh -c '"$0" --launch-immediately & exec xvfb-run -a python3 /probe/webkit_atspi_text_attributes.py "$1"' "${launcher}" "${URL}"
EOF

status=0
summary=()
for engine in "${ENGINES[@]}"; do
  tag="paintedwolf-webkit-atspi:${engine}"
  echo "== WebKitGTK ${engine} (${IMAGE[${engine}]})"
  if ! printf 'FROM %s\nRUN %s\n' "${IMAGE[${engine}]}" "${INSTALL[${engine}]}" | "${RUNTIME}" build -q -t "${tag}" - >/dev/null; then
    echo "den:webkit:atspi: unverified: could not build the WebKitGTK ${engine} image" >&2
    summary+=("${engine} unverified")
    ((status == 1)) || status=2
    continue
  fi
  result=0
  "${RUNTIME}" run --rm --network host --shm-size 512m \
    -v "${DIR}/harness:/probe:ro" -e EXPECTED="${engine}" -e URL="${LYCAON_E2E_BASE_URL}" \
    "${tag}" sh -c "${SESSION}" || result=$?
  case "${result}" in
    0) summary+=("${engine} passed") ;;
    1) summary+=("${engine} failed"); status=1 ;;
    *) summary+=("${engine} unverified"); ((status == 1)) || status=2 ;;
  esac
done

if ((status == 1)) && [[ -f "${LYCAON_E2E_STATE_DIR}/sidecar.log" ]]; then
  echo "den:webkit:atspi: sidecar log tail" >&2
  tail -n 40 "${LYCAON_E2E_STATE_DIR}/sidecar.log" >&2
fi
(IFS=","; echo "den:webkit:atspi: ${summary[*]}")
exit "${status}"

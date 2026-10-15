#!/usr/bin/env bash
# Asks each supported WebKitGTK release for AT-SPI text attributes across the harness Den, as Orca does (Linux).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" != "--supervised" ]]; then
  exec python3 "${DIR}/harness/supervise.py" "$0" "$@"
fi
shift

# Each WebKitGTK release's image is defined by the distribution release that ships it.
IMAGES="${DIR}/harness/webkit-atspi"
ENGINES=()
for definition in "${IMAGES}"/*.Containerfile; do
  ENGINES+=("$(basename "${definition}" .Containerfile)")
done

# --prepare builds the missing images and runs nothing, for a lane that later runs offline.
PREPARE=0
if [[ "${1:-}" == "--prepare" ]]; then
  PREPARE=1
  shift
fi
if (($#)); then
  for engine in "$@"; do
    [[ -f "${IMAGES}/${engine}.Containerfile" ]] || { echo "error: unknown WebKitGTK release ${engine}; known: ${ENGINES[*]}" >&2; exit 2; }
  done
  ENGINES=("$@")
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "den:webkit:atspi: unverified: WebKitGTK accessibility runs on Linux only" >&2
  exit 2
fi
# Podman runs in the caller's namespaces, so the probe's host network is the harness Den's,
# including inside a lane's loopback-only namespace; a docker daemon's host network is its own.
RUNTIME=""
for candidate in podman docker; do
  if command -v "${candidate}" >/dev/null && "${candidate}" info >/dev/null 2>&1; then
    RUNTIME="${candidate}"
    break
  fi
done
if [[ -z "${RUNTIME}" ]]; then
  echo "den:webkit:atspi: unverified: needs a working docker or podman to run each WebKitGTK release" >&2
  exit 2
fi

# Tags follow each definition's digest, so a definition change builds a new image and an
# unchanged one is reused offline.
image_tag() {
  echo "paintedwolf-webkit-atspi:$1-$(sha256sum "${IMAGES}/$1.Containerfile" | cut -c1-12)"
}

# Echoes the tag of the release's image, building it when missing.
ensure_image() {
  local tag
  tag="$(image_tag "$1")"
  if ! "${RUNTIME}" image inspect "${tag}" >/dev/null 2>&1; then
    "${RUNTIME}" build -q -t "${tag}" -f "${IMAGES}/$1.Containerfile" "${IMAGES}" >/dev/null || return 1
  fi
  echo "${tag}"
}

if ((PREPARE)); then
  status=0
  for engine in "${ENGINES[@]}"; do
    if ensure_image "${engine}" >/dev/null; then
      echo "den:webkit:atspi: WebKitGTK ${engine} image ready"
    else
      echo "den:webkit:atspi: unverified: could not build the WebKitGTK ${engine} image" >&2
      status=2
    fi
  done
  exit "${status}"
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
  *) echo "unverified: the image ships WebKitGTK ${installed}, not ${EXPECTED}; update its image definition" >&2; exit 2 ;;
esac
exec dbus-run-session -- sh -c '"$0" --launch-immediately & exec xvfb-run -a python3 /probe/webkit_atspi_text_attributes.py "$1"' "${launcher}" "${URL}"
EOF

status=0
summary=()
for engine in "${ENGINES[@]}"; do
  echo "== WebKitGTK ${engine} ($(sed -n 's/^FROM //p' "${IMAGES}/${engine}.Containerfile"))"
  if ! tag="$(ensure_image "${engine}")"; then
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

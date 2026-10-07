#!/usr/bin/env bash
# Publish the mutable updater pointer and verify custom-domain bytes.
set -euo pipefail

FILE=""
CHANNEL=""
GENERATION=""
KEY=""
STORAGE_PREFIX=""
FROM_VERSION=""
ADVANCE_IF_NEWER=0
WRANGLER=(bunx wrangler@4.115.0)

usage() {
  echo "Usage: release-r2-publish-pointer.sh --file PATH --channel {stable|preview} [--generation NUMBER] [--storage-prefix PREFIX] [--from-version VERSION] [--advance-if-newer]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --file) FILE="${2:-}"; shift 2 ;;
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --generation) GENERATION="${2:-}"; shift 2 ;;
    --storage-prefix) STORAGE_PREFIX="${2:-}"; shift 2 ;;
    --from-version) FROM_VERSION="${2:-}"; shift 2 ;;
    --advance-if-newer) ADVANCE_IF_NEWER=1; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "${FILE}" ]] || usage
[[ -n "${GENERATION}" ]] || GENERATION="$(jq -er '.update_keys.signing_generation' "${FILE}")"
# Clients accept a pointer only with a signature from this generation's feed key.
: "${FEED_SIGNING_PRIVATE_KEY:?missing FEED_SIGNING_PRIVATE_KEY}"
[[ "${GENERATION}" =~ ^[1-9][0-9]*$ ]] || usage
case "${CHANNEL}" in
  stable|preview) KEY="updates/${CHANNEL}/key-${GENERATION}/latest.json" ;;
  *) echo "error: channel must be stable or preview" >&2; usage ;;
esac
[[ -f "${FILE}" ]] || { echo "error: pointer source not found: ${FILE}" >&2; exit 1; }
[[ "${KEY}" != /* && "${KEY}" != */ && "${KEY}" != *..* && "${KEY}" != *//* ]] || {
  echo "error: updater pointer key must be a normalized relative path" >&2
  exit 1
}
if [[ -n "${STORAGE_PREFIX}" ]]; then
  [[ "${STORAGE_PREFIX}" != /* && "${STORAGE_PREFIX}" != */ && \
     "${STORAGE_PREFIX}" != *..* && "${STORAGE_PREFIX}" != *//* ]] || {
    echo "error: storage prefix must be a normalized relative path" >&2
    exit 1
  }
fi
OBJECT_KEY="${KEY}"
if [[ -n "${STORAGE_PREFIX}" ]]; then
  OBJECT_KEY="${STORAGE_PREFIX}/${KEY}"
fi
: "${R2_BUCKET:?missing R2_BUCKET}"
: "${CLOUDFLARE_ACCOUNT_ID:?missing CLOUDFLARE_ACCOUNT_ID}"
: "${CLOUDFLARE_API_TOKEN:?missing CLOUDFLARE_API_TOKEN}"
: "${DOWNLOAD_BASE_URL:?missing DOWNLOAD_BASE_URL}"

bash "$(dirname "$0")/release-validate-updater-manifest.sh" --file "${FILE}"
python3 - "$(dirname "$0")" "${FILE}" "${GENERATION}" "${FROM_VERSION}" <<'PYKEY'
import json, sys
sys.path.insert(0, sys.argv[1])
from update_keys import load_registry, validate_publication
validate_publication(load_registry(), json.load(open(sys.argv[2])), int(sys.argv[3]), halt=bool(sys.argv[4]))
PYKEY


WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/release-pointer.XXXXXX")"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT
PUBLIC="${WORKDIR}/public.json"
CURRENT="${WORKDIR}/current.json"
EXPECTED_VERSION="$(jq -r '.version' "${FILE}")"
if [[ -n "${FROM_VERSION}" ]]; then
  python3 "$(dirname "$0")/semver-compare.py" eq \
    "${FROM_VERSION}" "${FROM_VERSION}" >/dev/null
fi

# Both source-generation channels constrain the bridge version.
if [[ -z "${FROM_VERSION}" && "$(jq -r '.update_keys.embedded_generation' "${FILE}")" != "${GENERATION}" ]]; then
  for bridge_channel in stable preview; do
    bridge_key="updates/${bridge_channel}/key-${GENERATION}/latest.json"
    [[ -z "${STORAGE_PREFIX}" ]] || bridge_key="${STORAGE_PREFIX}/${bridge_key}"
    bridge_status="$(python3 "$(dirname "$0")/release_distribution.py" storage-read \
      --key "${bridge_key}" --output "${WORKDIR}/bridge-current.json")"
    case "${bridge_status}" in
      200)
        python3 - "$(dirname "$0")" "${FILE}" "${WORKDIR}/bridge-current.json" <<'PYBRIDGE'
import json, sys
sys.path.insert(0, sys.argv[1])
from update_keys import validate_bridge_advance
validate_bridge_advance(json.load(open(sys.argv[2])), json.load(open(sys.argv[3])))
PYBRIDGE
        ;;
      404) ;;
      *) echo "error: failed reading source generation ${bridge_channel} feed" >&2; exit 1 ;;
    esac
  done
fi

if [[ -z "${FROM_VERSION}" ]]; then
  advance_args=()
  [[ "${ADVANCE_IF_NEWER}" -eq 0 ]] || advance_args+=(--optional)
  advance="$(python3 "$(dirname "$0")/release_distribution.py" advance --file "${FILE}" \
    --key "${OBJECT_KEY}" "${advance_args[@]}")"
  if [[ "${advance}" == false ]]; then
    echo "release-pointer: ${CHANNEL} already advertises a newer release; no advance needed" >&2
    exit 0
  fi
  python3 "$(dirname "$0")/release_distribution.py" activate --file "${FILE}" \
    --output "${WORKDIR}/active.json" --storage-prefix "${STORAGE_PREFIX}"
  FILE="${WORKDIR}/active.json"
fi
# The signature binds the pointer's channel and generation through its file name, the
# announced version, and a timestamp clients require to advance.
FEED_NAME="latest-${CHANNEL}-key-${GENERATION}.json"
SIGNED_COPY="${WORKDIR}/${FEED_NAME}"
SIGNATURE="${SIGNED_COPY}.sig"
cp "${FILE}" "${SIGNED_COPY}"
(
  cd "$(dirname "$0")/../lycaon-den"
  TAURI_SIGNING_PRIVATE_KEY="${FEED_SIGNING_PRIVATE_KEY}" \
  TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${FEED_SIGNING_PRIVATE_KEY_PASSWORD:-}" \
    bun run tauri signer sign --app-version "${EXPECTED_VERSION}" "${SIGNED_COPY}" >/dev/null
)
rehearsal_args=()
[[ -z "${STORAGE_PREFIX}" ]] || rehearsal_args+=(--rehearsal)
python3 "$(dirname "$0")/feed_signature.py" --signature "${SIGNATURE}" --file "${FEED_NAME}" \
  --version "${EXPECTED_VERSION}" --generation "${GENERATION}" "${rehearsal_args[@]}" >/dev/null

publish_signature() {
  "${WRANGLER[@]}" r2 object put "${R2_BUCKET}/${OBJECT_KEY}.sig" \
    --file "${SIGNATURE}" --content-type text/plain \
    --cache-control "no-cache, no-store, must-revalidate" --remote
}
get_status="$(python3 "$(dirname "$0")/release_distribution.py" storage-read \
  --key "${OBJECT_KEY}" --output "${CURRENT}")"
case "${get_status}" in
  200)
    if python3 -c 'import json,sys; sys.exit(json.load(open(sys.argv[1])) != json.load(open(sys.argv[2])))' "${FILE}" "${CURRENT}"; then
      echo "release-pointer: authenticated pointer already has the requested bytes" >&2
      publish_signature
    else
      bash "$(dirname "$0")/release-validate-updater-manifest.sh" --file "${CURRENT}" --existing
      current_version="$(jq -r '.version' "${CURRENT}")"
      if [[ -n "${FROM_VERSION}" ]]; then
        [[ "${current_version}" == "${FROM_VERSION}" ]] || {
          echo "error: updater pointer changed to ${current_version}; expected ${FROM_VERSION}" >&2
          exit 1
        }
      else
        if ! python3 "$(dirname "$0")/semver-compare.py" gt \
          "${EXPECTED_VERSION}" "${current_version}" >/dev/null; then
          if [[ "${ADVANCE_IF_NEWER}" -eq 1 ]] && \
             python3 "$(dirname "$0")/semver-compare.py" gt \
               "${current_version}" "${EXPECTED_VERSION}" >/dev/null; then
            echo "release-pointer: ${CHANNEL} already advertises ${current_version}; no advance needed" >&2
            exit 0
          fi
          echo "error: updater pointer may only advance (${current_version} → ${EXPECTED_VERSION})" >&2
          exit 1
        fi
      fi
      publish_signature
      "${WRANGLER[@]}" r2 object put "${R2_BUCKET}/${OBJECT_KEY}" \
        --file "${FILE}" --content-type application/json \
        --cache-control "no-cache, no-store, must-revalidate" --remote
    fi
    ;;
  404)
    [[ -z "${FROM_VERSION}" ]] || {
      echo "error: updater pointer disappeared; expected active ${FROM_VERSION}" >&2
      exit 1
    }
    publish_signature
    "${WRANGLER[@]}" r2 object put "${R2_BUCKET}/${OBJECT_KEY}" \
      --file "${FILE}" --content-type application/json \
      --cache-control "no-cache, no-store, must-revalidate" --remote
    ;;
  *)
    echo "error: R2 returned HTTP ${get_status} reading current updater pointer" >&2
    exit 1
    ;;
esac
for attempt in $(seq 1 60); do
  if curl --fail --silent --show-error --location --connect-timeout 10 --max-time 30 \
    -H 'Cache-Control: no-cache' \
    "${DOWNLOAD_BASE_URL%/}/${KEY}" -o "${PUBLIC}" --dump-header "${WORKDIR}/headers" \
    && python3 "$(dirname "$0")/release_pointer_headers.py" "${WORKDIR}/headers" \
    && cmp -s "${FILE}" "${PUBLIC}" \
    && [[ "$(jq -r '.version // empty' "${PUBLIC}" 2>/dev/null)" == "${EXPECTED_VERSION}" ]] \
    && curl --fail --silent --show-error --location --connect-timeout 10 --max-time 30 \
      -H 'Cache-Control: no-cache' "${DOWNLOAD_BASE_URL%/}/${KEY}.sig" -o "${PUBLIC}.sig" \
    && cmp -s "${SIGNATURE}" "${PUBLIC}.sig"; then
    echo "release-pointer: public ${KEY} and its signature verified at version ${EXPECTED_VERSION}" >&2
    exit 0
  fi
  sleep 5
done

echo "error: public ${KEY} did not converge to the published bytes/version" >&2
exit 1

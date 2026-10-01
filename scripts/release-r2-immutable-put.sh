#!/usr/bin/env bash
# Create a versioned object once or accept identical bytes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=release-r2-rest.sh
source "${SCRIPT_DIR}/release-r2-rest.sh"

KEY=""
FILE=""
CONTENT_TYPE="application/octet-stream"
CACHE_CONTROL="public, max-age=31536000, immutable"
WRANGLER=(bunx wrangler@4.115.0)

usage() {
  echo "Usage: release-r2-immutable-put.sh --key KEY --file PATH [--content-type TYPE] [--cache-control VALUE]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --key) KEY="${2:-}"; shift 2 ;;
    --file) FILE="${2:-}"; shift 2 ;;
    --content-type) CONTENT_TYPE="${2:-}"; shift 2 ;;
    --cache-control) CACHE_CONTROL="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "${KEY}" && -n "${FILE}" ]] || usage
[[ -f "${FILE}" ]] || { echo "error: immutable source not found: ${FILE}" >&2; exit 1; }
[[ "${KEY}" != /* && "${KEY}" != */ && "${KEY}" != *..* && "${KEY}" != *//* ]] || {
  echo "error: immutable key must be a normalized relative path" >&2
  exit 1
}
r2_rest_require_env

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/release-r2-put.XXXXXX")"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT
EXISTING="${WORKDIR}/existing"
LOCAL_MD5="$(r2_rest_file_md5 "${FILE}")"

existing_stat="$(r2_rest_object_stat "${KEY}")"
if [[ -n "${existing_stat}" ]]; then
  existing_etag="$(printf '%s' "${existing_stat}" | r2_rest_etag)"
  if [[ "${existing_etag}" == "${LOCAL_MD5}" ]]; then
    echo "release-r2-put: ${KEY} already contains identical bytes — ok" >&2
    exit 0
  fi
  echo "error: immutable R2 object ${KEY} already exists with different bytes" >&2
  exit 1
fi

# Serialized callers make the list-before-write check authoritative.
"${WRANGLER[@]}" r2 object put "${R2_BUCKET}/${KEY}" \
  --file "${FILE}" --content-type "${CONTENT_TYPE}" \
  --cache-control "${CACHE_CONTROL}" --remote

verify_etag=""
for _ in $(seq 1 30); do
  verify_stat="$(r2_rest_object_stat "${KEY}")"
  verify_etag="$(printf '%s' "${verify_stat}" | r2_rest_etag)"
  [[ "${verify_etag}" == "${LOCAL_MD5}" ]] && break
  sleep 1
done
[[ "${verify_etag}" == "${LOCAL_MD5}" ]] || {
  echo "error: R2 list did not show ${KEY} with matching bytes after upload" >&2
  exit 1
}

verify_status="$(r2_rest_get "${KEY}" "${EXISTING}")" || {
  echo "error: transport failure verifying immutable R2 object ${KEY}" >&2
  exit 1
}
case "${verify_status}" in
  200)
    cmp -s "${FILE}" "${EXISTING}" || {
      echo "error: immutable R2 object ${KEY} differs immediately after upload" >&2
      exit 1
    }
    ;;
  404)
    # The listing binds the checksum while GET may still return a cached miss.
    ;;
  *)
    echo "error: R2 returned HTTP ${verify_status} verifying immutable object ${KEY}" >&2
    exit 1
    ;;
esac
echo "release-r2-put: created and verified ${KEY}" >&2

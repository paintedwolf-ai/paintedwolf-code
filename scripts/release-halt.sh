#!/usr/bin/env bash
# Withdraw a release from every affected update feed.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHANNEL=""
GENERATION=""
HALT_PLAN=""
PREPARE_OUTPUT=""
APPLY_PREPARED=""
DISTRIBUTION_OUTPUT=""
REVIEWED_PLAN=0
MANIFEST_KEY=""
RELEASE_PREFIX="updates/releases"
BAD_VERSION=""
LAST_GOOD_VERSION=""
CURRENT_FILE=""
RELEASE_FILE=""
DRY_RUN=0
SELF_TEST=0
STORAGE_PREFIX=""

usage() {
  cat >&2 <<'EOF'
Usage: release-halt.sh --bad VERSION [--last-good VERSION] [--dry-run]
       release-halt.sh --plan PATH [--dry-run]
       release-halt.sh --self-test

Withdraws BAD from all affected feeds. LAST_GOOD restores an older release;
without it, affected feeds stop offering updates. Retries verify current state.
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prepare-output) PREPARE_OUTPUT="${2:-}"; shift 2 ;;
    --apply-prepared) APPLY_PREPARED="${2:-}"; shift 2 ;;
    --bad) BAD_VERSION="${2:-}"; shift 2 ;;
    --last-good) LAST_GOOD_VERSION="${2:-}"; shift 2 ;;
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --generation) GENERATION="${2:-}"; shift 2 ;;
    --plan) HALT_PLAN="${2:-}"; shift 2 ;;
    --distribution-output) DISTRIBUTION_OUTPUT="${2:-}"; shift 2 ;;
    --reviewed-plan) REVIEWED_PLAN=1; shift ;;
    --current-file) CURRENT_FILE="${2:-}"; shift 2 ;;
    --release-file) RELEASE_FILE="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --self-test) SELF_TEST=1; shift ;;
    --storage-prefix) STORAGE_PREFIX="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

run_self_test() {
  local fixtures="${ROOT}/lycaon/testdata/release-halt"
  DOWNLOAD_BASE_URL=https://downloads.paintedwolf.dev bash "$0" \
    --channel stable \
    --bad 0.2.0 --last-good 0.1.0 \
    --current-file "${fixtures}/current-bad.json" \
    --release-file "${fixtures}/last-good.json" --dry-run >/dev/null
  python3 "${ROOT}/scripts/test_update_keys.py"
  echo "release-halt --self-test passed" >&2
}

if [[ "${SELF_TEST}" -eq 1 ]]; then
  run_self_test
  exit 0
fi

if [[ -n "${HALT_PLAN}" ]]; then
  plan_args=(--plan "${HALT_PLAN}")
  [[ -z "${PREPARE_OUTPUT}" ]] || plan_args+=(--prepare-output "${PREPARE_OUTPUT}")
  [[ -z "${APPLY_PREPARED}" ]] || plan_args+=(--apply-prepared "${APPLY_PREPARED}")
  [[ "${DRY_RUN}" -eq 0 ]] || plan_args+=(--dry-run)
  [[ -z "${DISTRIBUTION_OUTPUT}" ]] || plan_args+=(--distribution-output "${DISTRIBUTION_OUTPUT}")
  exec python3 "${ROOT}/scripts/release-halt-plan.py" "${plan_args[@]}"
fi

[[ -n "${BAD_VERSION}" ]] || usage
if [[ "${REVIEWED_PLAN}" -eq 0 && -z "${CURRENT_FILE}" && -z "${STORAGE_PREFIX}" ]]; then
  plan_args=(--bad "${BAD_VERSION}" --source-generation "${GENERATION:-1}")
  [[ -z "${LAST_GOOD_VERSION}" ]] || plan_args+=(--last-good "${LAST_GOOD_VERSION}")
  [[ "${DRY_RUN}" -eq 0 ]] || plan_args+=(--dry-run)
  [[ -z "${DISTRIBUTION_OUTPUT}" ]] || plan_args+=(--distribution-output "${DISTRIBUTION_OUTPUT}")
  exec python3 "${ROOT}/scripts/release-halt-plan.py" "${plan_args[@]}"
fi
[[ -n "${GENERATION}" ]] || GENERATION="$(jq -r .signing_generation "${ROOT}/packaging/update-keys.json")"
[[ "${GENERATION}" =~ ^[1-9][0-9]*$ ]] || usage
case "${CHANNEL}" in
  stable|preview) MANIFEST_KEY="updates/${CHANNEL}/key-${GENERATION}/latest.json" ;;
  *) echo "error: channel must be stable or preview" >&2; usage ;;
esac
if [[ -n "${LAST_GOOD_VERSION}" ]]; then
python3 "${ROOT}/scripts/semver-compare.py" gt "${BAD_VERSION}" "${LAST_GOOD_VERSION}" >/dev/null || {
  echo "error: bad version ${BAD_VERSION} must be newer than last-good ${LAST_GOOD_VERSION}" >&2
  exit 1
}
fi
: "${DOWNLOAD_BASE_URL:?missing DOWNLOAD_BASE_URL}"
if [[ -n "${STORAGE_PREFIX}" ]]; then
  [[ "${STORAGE_PREFIX}" != /* && "${STORAGE_PREFIX}" != */ && \
     "${STORAGE_PREFIX}" != *..* && "${STORAGE_PREFIX}" != *//* ]] || {
    echo "error: storage prefix must be a normalized relative path" >&2
    exit 1
  }
fi

storage_key() {
  if [[ -n "${STORAGE_PREFIX}" ]]; then
    printf '%s/%s\n' "${STORAGE_PREFIX}" "$1"
  else
    printf '%s\n' "$1"
  fi
}

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/release-halt.XXXXXX")"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT
CURRENT_LOCAL="${WORKDIR}/current.json"
RELEASE_LOCAL="${WORKDIR}/last-good.json"

r2_get() {
  local key="$1" output="$2" status
  status="$(python3 "${ROOT}/scripts/release_distribution.py" storage-read --key "${key}" --output "${output}")"

  case "${status}" in
    200) ;;
    404) echo "error: R2 object ${key} does not exist" >&2; return 1 ;;
    *) echo "error: R2 returned HTTP ${status} reading object ${key}" >&2; return 1 ;;
  esac
}

if [[ -n "${CURRENT_FILE}" || -n "${RELEASE_FILE}" ]]; then
  [[ -n "${CURRENT_FILE}" && "${DRY_RUN}" -eq 1 && ( -z "${LAST_GOOD_VERSION}" || -n "${RELEASE_FILE}" ) ]] || {
    echo "error: local dry-run needs a current file and, for a replacement, a release file" >&2
    exit 1
  }
  [[ -f "${CURRENT_FILE}" && ( -z "${LAST_GOOD_VERSION}" || -f "${RELEASE_FILE}" ) ]] || {
    echo "error: halt fixture file not found" >&2
    exit 1
  }
  cp "${CURRENT_FILE}" "${CURRENT_LOCAL}"
  if [[ -n "${LAST_GOOD_VERSION}" ]]; then
    cp "${RELEASE_FILE}" "${RELEASE_LOCAL}"
  else
    jq '.withdrawn = true' "${CURRENT_LOCAL}" > "${RELEASE_LOCAL}"
  fi
else
  r2_get "$(storage_key "${MANIFEST_KEY}")" "${CURRENT_LOCAL}"
  if [[ -n "${LAST_GOOD_VERSION}" ]]; then
    r2_get "$(storage_key "${RELEASE_PREFIX}/${LAST_GOOD_VERSION}.json")" "${RELEASE_LOCAL}"
  else
    jq '.withdrawn = true' "${CURRENT_LOCAL}" > "${RELEASE_LOCAL}"
  fi
fi

bash "${ROOT}/scripts/release-validate-updater-manifest.sh" --file "${CURRENT_LOCAL}" --existing
bash "${ROOT}/scripts/release-validate-updater-manifest.sh" \
  --file "${RELEASE_LOCAL}" --version "${LAST_GOOD_VERSION:-${BAD_VERSION}}"
for manifest in "${CURRENT_LOCAL}" "${RELEASE_LOCAL}"; do
  [[ "$(jq -r '.update_keys.signing_generation' "${manifest}")" == "${GENERATION}" ]] || {
    echo "error: halt cannot point a generation at a release signed by another key" >&2; exit 1;
  }
done
if [[ "$(jq -r '.update_keys.embedded_generation' "${CURRENT_LOCAL}")" != "${GENERATION}" && "${REVIEWED_PLAN}" -ne 1 ]]; then
  echo "error: a bridge halt requires --plan covering both source channels and every existing successor feed" >&2
  exit 1
fi
ACTIVE_VERSION="$(jq -r '.version' "${CURRENT_LOCAL}")"
if [[ "${ACTIVE_VERSION}" != "${BAD_VERSION}" && "${ACTIVE_VERSION}" != "${LAST_GOOD_VERSION}" ]]; then
  echo "error: active updater version is ${ACTIVE_VERSION}; expected bad ${BAD_VERSION} (or last-good ${LAST_GOOD_VERSION} on retry)" >&2
  exit 1
fi

if [[ -n "${LAST_GOOD_VERSION}" && "$(jq -r '.withdrawn // false' "${RELEASE_LOCAL}")" == true ]]; then
  echo "error: replacement release is withdrawn" >&2
  exit 1
fi
if [[ "${CHANNEL}" == stable && -n "${LAST_GOOD_VERSION}" ]]; then
  python3 - "${ROOT}/scripts" "${LAST_GOOD_VERSION}" <<'PYSTABLE'
import sys
sys.path.insert(0, sys.argv[1])
from release_semver import parse
if parse(sys.argv[2]).channel != "stable":
    raise ValueError("Stable cannot restore a prerelease")
PYSTABLE
fi

if [[ "${DRY_RUN}" -eq 1 && -z "${CURRENT_FILE}" && -z "${PREPARE_OUTPUT}" ]]; then
  PREPARE_OUTPUT="${WORKDIR}/prepared"
fi
if [[ "${DRY_RUN}" -eq 1 && -z "${PREPARE_OUTPUT}" ]]; then
  echo "release-halt: dry-run ok — verified ${BAD_VERSION} → ${LAST_GOOD_VERSION:-withdrawn}" >&2
  exit 0
fi

prepare_args=()
[[ -z "${PREPARE_OUTPUT}" ]] || prepare_args+=(--prepare-output "${PREPARE_OUTPUT}")
bash "${ROOT}/scripts/release-r2-publish-pointer.sh" \
  --file "${RELEASE_LOCAL}" \
  --channel "${CHANNEL}" --generation "${GENERATION}" --storage-prefix "${STORAGE_PREFIX}" \
  --from-version "${BAD_VERSION}" ${prepare_args[@]+"${prepare_args[@]}"}
[[ -z "${PREPARE_OUTPUT}" ]] || exit 0
echo "release-halt: stopped new direct-download discovery of ${BAD_VERSION}; ${CHANNEL} now advertises ${LAST_GOOD_VERSION:-no release}" >&2
echo "release-halt: installed ${BAD_VERSION} clients remain installed; publish a higher signed recovery release" >&2

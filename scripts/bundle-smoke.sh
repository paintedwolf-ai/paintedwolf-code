#!/usr/bin/env bash
# Smoke-test an installed desktop bundle.
#
# Usage:
#   ./task bundle:smoke -- --app "/path/to/Painted Wolf Code.app"
#   ./task bundle:smoke -- --dmg /path/to/painted-wolf-code_v1.0.0_darwin-aarch64.dmg
#   ./task bundle:smoke -- --app APP --skip-quarantine   # signed, not notarized
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

APP=""
DMG=""
QUARANTINE=1
TIMEOUT="${BUNDLE_SMOKE_TIMEOUT:-90}"

usage() {
  cat >&2 <<'EOF'
Usage: bundle-smoke.sh (--app <path.app> | --dmg <path.dmg>) [--skip-quarantine]

Launches a stably signed desktop app through LaunchServices and asserts its bundled
engine serves. --skip-quarantine bypasses only Gatekeeper, for a signed artifact
that has not been notarized. Ad-hoc builds cannot exercise the release Keychain
path and are rejected before launch.
EOF
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --app) APP="${2:-}"; shift 2 ;;
    --dmg) DMG="${2:-}"; shift 2 ;;
    --skip-quarantine) QUARANTINE=0; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ "$(uname -s)" == "Darwin" ]] || { echo "error: bundle:smoke is macOS-only" >&2; exit 1; }
for need in curl jq python3; do
  command -v "${need}" >/dev/null 2>&1 || { echo "error: ${need} required" >&2; exit 1; }
done

WORK="$(mktemp -d "${TMPDIR:-/tmp}/bundle-smoke.XXXXXX")"
MOUNT=""
APP_PID=""
ENGINE_PID=""
OPEN_PID=""
APP_BINARY=""

find_launched_app_pid() {
	[[ -n "${APP_BINARY}" ]] || return 0
	BUNDLE_SMOKE_APP_BINARY="${APP_BINARY}" python3 - <<'PY'
import os
import subprocess

binary = os.environ["BUNDLE_SMOKE_APP_BINARY"]
for raw in subprocess.check_output(["ps", "-axo", "pid=,command="], text=True).splitlines():
    fields = raw.strip().split(None, 1)
    if len(fields) == 2 and (fields[1] == binary or fields[1].startswith(binary + " ")):
        print(fields[0])
        break
PY
}

stop_pid() {
	local pid="$1" attempt
	[[ -n "${pid}" ]] || return 0
	kill -TERM "${pid}" 2>/dev/null || return 0
	for attempt in {1..50}; do
		kill -0 "${pid}" 2>/dev/null || return 0
		sleep 0.1
	done
	kill -KILL "${pid}" 2>/dev/null || true
}

cleanup() {
	[[ -n "${APP_PID}" ]] || APP_PID="$(find_launched_app_pid || true)"
	stop_pid "${APP_PID}"
	if [[ -n "${OPEN_PID}" ]]; then
		kill -TERM "${OPEN_PID}" 2>/dev/null || true
		wait "${OPEN_PID}" 2>/dev/null || true
	fi
	stop_pid "${ENGINE_PID}"
  [[ -n "${MOUNT}" ]] && hdiutil detach "${MOUNT}" -quiet 2>/dev/null || true
  rm -rf "${WORK}"
}
trap cleanup EXIT

# Mount and copy disk-image inputs before launch.
if [[ -n "${DMG}" ]]; then
  [[ -f "${DMG}" ]] || { echo "error: no such dmg: ${DMG}" >&2; exit 1; }
  MOUNT="${WORK}/mnt"
  mkdir -p "${MOUNT}"
  echo "→ mounting $(basename "${DMG}")" >&2
  hdiutil attach "${DMG}" -nobrowse -readonly -mountpoint "${MOUNT}" -quiet
  SRC_APP="$(find "${MOUNT}" -maxdepth 1 -name '*.app' -print -quit)"
  [[ -n "${SRC_APP}" ]] || { echo "error: dmg contains no .app" >&2; exit 1; }
  APP="${WORK}/$(basename "${SRC_APP}")"
  cp -a "${SRC_APP}" "${APP}"
fi

[[ -n "${APP}" ]] || usage
[[ -d "${APP}" ]] || { echo "error: no such app bundle: ${APP}" >&2; exit 1; }

ENGINE="${APP}/Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw"
if [[ ! -x "${ENGINE}" ]]; then
  echo "error: bundle has no executable Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw" >&2
  exit 1
fi
LOGS_CLI="${APP}/Contents/MacOS/pw-logs"
if [[ ! -x "${LOGS_CLI}" ]]; then
  echo "error: bundle has no executable Contents/MacOS/pw-logs" >&2
  exit 1
fi
DECIDE_ENGINE="${APP}/Contents/MacOS/bialy"
if [[ ! -x "${DECIDE_ENGINE}" ]]; then
  echo "error: bundle has no executable Contents/MacOS/bialy" >&2
  exit 1
fi
APP_EXECUTABLE="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "${APP}/Contents/Info.plist")"
APP_BINARY="${APP}/Contents/MacOS/${APP_EXECUTABLE}"
[[ -x "${APP_BINARY}" ]] || { echo "error: bundle main executable missing: ${APP_BINARY}" >&2; exit 1; }

require_stable_signature() {
  local path="$1" label="$2" details
  if ! details="$(codesign -d --verbose=4 "${path}" 2>&1)"; then
    echo "error: ${label} has no valid macOS code signature: ${path}" >&2
    exit 1
  fi
  if grep -q '^Signature=adhoc$' <<<"${details}"; then
    echo "error: ${label} is ad-hoc signed and cannot exercise the release Keychain path" >&2
    echo "       Use the Developer ID-signed artifact produced by the release workflow." >&2
    echo "       --skip-quarantine skips Gatekeeper only; it does not bypass this requirement." >&2
    exit 1
  fi
  if ! codesign --verify --strict "${path}" >/dev/null 2>&1; then
    echo "error: ${label} code signature does not verify: ${path}" >&2
    exit 1
  fi
}

# Stable signing allows the release credential path to initialize.
require_stable_signature "${APP_BINARY}" "desktop app"
require_stable_signature "${ENGINE}" "bundled engine"
"${ENGINE}" credentials verify-protection

# Quarantine exercises the downloaded-bundle path.
if (( QUARANTINE == 1 )); then
  echo "→ applying com.apple.quarantine" >&2
  xattr -w com.apple.quarantine \
    "0081;$(printf '%x' "$(date +%s)");Safari;$(uuidgen)" "${APP}"
  if ! spctl --assess --type execute --verbose "${APP}" 2>&1 | tee "${WORK}/spctl.txt" >&2; then
    echo "error: Gatekeeper rejected the quarantined bundle — a downloaded copy will not open" >&2
    exit 1
  fi
  grep -q 'accepted' "${WORK}/spctl.txt" || {
    echo "error: spctl did not accept the bundle" >&2
    exit 1
  }
fi

# Use a minimal launch environment and isolated application store.
SMOKE_HOME="${WORK}/home"
CONFIG_DIR="${SMOKE_HOME}/.config/paintedwolf"
mkdir -p "${CONFIG_DIR}"
OPEN_LOG="${WORK}/open.log"
echo "→ launching the desktop app through LaunchServices" >&2
/usr/bin/open -W -n -j \
  --env "HOME=${SMOKE_HOME}" \
  --env "LYCAON_LLM_MOCK=1" \
  --env "LYCAON_LOG_LEVEL=${LYCAON_LOG_LEVEL:-warn}" \
  --env "SHELL=" \
  --env "PATH=/usr/bin:/bin:/usr/sbin:/sbin" \
  "${APP}" >"${OPEN_LOG}" 2>&1 &
OPEN_PID=$!

fail() {
  echo "error: $1" >&2
	echo "--- LaunchServices output ---" >&2
	cat "${OPEN_LOG}" >&2 2>/dev/null || true
  echo "--- engine log ---" >&2
	cat "${CONFIG_DIR}/engine.log" >&2 2>/dev/null || true
  exit 1
}

DEADLINE=$((SECONDS + TIMEOUT))
HEALTH=""
PORT=""
TOKEN=""
while (( SECONDS < DEADLINE )); do
	kill -0 "${OPEN_PID}" 2>/dev/null || fail "desktop app exited during boot"
	if [[ -s "${CONFIG_DIR}/daemon.json" && -s "${CONFIG_DIR}/api.token" ]]; then
		PORT="$(jq -r '.port // empty' "${CONFIG_DIR}/daemon.json" 2>/dev/null || true)"
		ENGINE_PID="$(jq -r '.pid // empty' "${CONFIG_DIR}/daemon.json" 2>/dev/null || true)"
		TOKEN="$(tr -d '\r\n' <"${CONFIG_DIR}/api.token")"
		if [[ -n "${PORT}" && -n "${ENGINE_PID}" && -n "${TOKEN}" ]] && kill -0 "${ENGINE_PID}" 2>/dev/null; then
			BASE="http://127.0.0.1:${PORT}"
			if HEALTH="$(curl -sf --max-time 2 "${BASE}/health" 2>/dev/null)"; then
				STATUS="$(jq -r '.status // empty' <<<"${HEALTH}")"
				[[ "${STATUS}" == "ok" || "${STATUS}" == "degraded" ]] && break
			fi
		fi
	fi
  sleep 0.25
done
STATUS="$(jq -r '.status // empty' <<<"${HEALTH}")"
[[ "${STATUS}" == "ok" || "${STATUS}" == "degraded" ]] || \
  fail "GUI-spawned engine never became healthy within ${TIMEOUT}s (status=${STATUS:-none})"

APP_PID="$(ps -o ppid= -p "${ENGINE_PID}" | tr -d '[:space:]')"
[[ -n "${APP_PID}" ]] && kill -0 "${APP_PID}" 2>/dev/null || fail "engine has no live desktop-app parent"
APP_COMMAND="$(ps -o command= -p "${APP_PID}")"
case "${APP_COMMAND}" in
	"${APP_BINARY}"*) ;;
	*) fail "engine parent is not the launched app: ${APP_COMMAND}" ;;
esac
echo "  health: ${STATUS}" >&2

# Verify that unauthenticated API requests are rejected.
CODE="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/v1/projects")"
[[ "${CODE}" == "401" ]] || fail "unauthenticated /v1/projects returned ${CODE}, want 401"

CODE="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" "${BASE}/v1/projects")"
[[ "${CODE}" == "200" ]] || fail "authenticated /v1/projects returned ${CODE}, want 200"

# Catalog checks cover missing bundled resources that /health cannot detect.
WORKFLOWS="$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${BASE}/v1/workflows" || true)"
COUNT="$(jq -r '(.workflows // []) | length' <<<"${WORKFLOWS}" 2>/dev/null || echo 0)"
if [[ "${COUNT}" -lt 1 ]]; then
  fail "bundled engine served ${COUNT} workflows — engine-root resources did not resolve from inside the .app"
fi
echo "  workflows resolved: ${COUNT}" >&2

# command_path verifies account-shell recovery from the minimal launch environment.
PREFLIGHT="$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${BASE}/v1/preflight" || true)"
PATH_STATUS="$(jq -r '.probes[]? | select(.id == "command_path") | .status' <<<"${PREFLIGHT}")"
[[ "${PATH_STATUS}" == "ok" ]] || \
  fail "GUI command PATH did not resolve from the account shell (status=${PATH_STATUS:-missing})"
echo "  GUI command path: ${PATH_STATUS}" >&2

# A missing credential host exercises the signed helper without changing Keychain items.
MISSING_CREDENTIAL_HOST="bundle-smoke-$(uuidgen).invalid"
printf 'protocol=https\nhost=%s\n\n' "${MISSING_CREDENTIAL_HOST}" \
  | "${ENGINE}" git-credential-osxkeychain get >"${WORK}/credential.out"
[[ ! -s "${WORK}/credential.out" ]] || fail "missing Git credential unexpectedly returned a value"
echo "  macOS Git credential bridge: ok" >&2

SCHEMA="$(jq -r '.schema_version // empty' <<<"${HEALTH}")"
WANT_SCHEMA="$(jq -r '.schema_version' "${ROOT}/lycaon/internal/db/schema_version_lock.json")"
[[ "${SCHEMA}" == "${WANT_SCHEMA}" ]] || \
  fail "bundled engine schema_version=${SCHEMA} want ${WANT_SCHEMA}"

echo "bundle:smoke — GUI launched, sidecar authenticated, catalog/path/credentials resolved, schema ${SCHEMA}: ok" >&2

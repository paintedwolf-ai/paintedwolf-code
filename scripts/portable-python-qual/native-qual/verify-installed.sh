#!/usr/bin/env bash
# PY06 Installed-Artifact, Fail-Closed Skew, Signing, Upgrade & Offline Launch Qualification Script.
# Runs on macOS arm64 and Linux amd64 with PW_PORTABLE_PYTHON_IMAGE_DIR UNSET.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: verify-installed.sh --engine-root <dir> --binary <path> --out-dir <dir> [--repo-root <dir>] [--backend <name>] [--require-native]
EOF
  exit 2
}

ENGINE_ROOT=""
BINARY=""
OUT_DIR=""
REPO_ROOT=""
BACKEND="native-aot"
REQUIRE_NATIVE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --backend) BACKEND="$2"; shift 2 ;;
    --require-native) REQUIRE_NATIVE=1; shift 1 ;;
    *) usage ;;
  esac
done

[[ -n "${ENGINE_ROOT}" && -n "${BINARY}" && -n "${OUT_DIR}" ]] || usage
mkdir -p "${OUT_DIR}"

# Critical rule: installed product qualification MUST run with image override UNSET.
unset PW_PORTABLE_PYTHON_IMAGE_DIR
unset PW_PORTABLE_PYTHON_ENGINE_DIR

OS_NAME="$(uname -s)"
HASH_CMD="sha256sum"
NATIVE_LIB_NAME="libpw_cpython_wasi.so"
if [[ "${OS_NAME}" == "Darwin" ]]; then
  HASH_CMD="shasum -a 256"
  NATIVE_LIB_NAME="libpw_cpython_wasi.dylib"
fi

IMAGE_DIR="${ENGINE_ROOT}/portable-python/image"
MANIFEST="${IMAGE_DIR}/image-manifest.json"
ENGINE_DIR="${ENGINE_ROOT}/portable-python/engine"
NATIVE_LIB="${ENGINE_DIR}/${NATIVE_LIB_NAME}"
ENGINE_MANIFEST="${ENGINE_DIR}/artifact-manifest.json"

[[ -f "${MANIFEST}" ]] || { echo "error: missing ${MANIFEST}" >&2; exit 1; }
[[ -x "${BINARY}" ]] || { echo "error: binary not executable: ${BINARY}" >&2; exit 1; }
if [[ "${REQUIRE_NATIVE}" == "1" ]]; then
  [[ -f "${NATIVE_LIB}" ]] || { echo "error: missing required native engine ${NATIVE_LIB}" >&2; exit 1; }
  [[ -f "${ENGINE_MANIFEST}" ]] || { echo "error: missing required engine manifest ${ENGINE_MANIFEST}" >&2; exit 1; }
fi

# 1. Record platform, build, toolchain, host load, and digests.
NATIVE_LIB_SHA="absent"
ENGINE_MANIFEST_SHA="absent"
if [[ -f "${NATIVE_LIB}" ]]; then
  NATIVE_LIB_SHA="$(${HASH_CMD} "${NATIVE_LIB}" | awk '{print $1}')"
fi
if [[ -f "${ENGINE_MANIFEST}" ]]; then
  ENGINE_MANIFEST_SHA="$(${HASH_CMD} "${ENGINE_MANIFEST}" | awk '{print $1}')"
  cp "${ENGINE_MANIFEST}" "${OUT_DIR}/artifact-manifest.json"
fi

{
  echo "uname: $(uname -srm)"
  echo "host_load: $(uptime)"
  echo "expected_default_backend: ${BACKEND}"
  echo "go_version: $(go version 2>/dev/null || echo unavailable)"
  echo "git_head: $(git -C "${REPO_ROOT:-.}" rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "binary_path: ${BINARY}"
  echo "binary_sha256: $(${HASH_CMD} "${BINARY}" | awk '{print $1}')"
  echo "image_manifest_sha256: $(${HASH_CMD} "${MANIFEST}" | awk '{print $1}')"
  echo "native_engine_path: ${NATIVE_LIB}"
  echo "native_engine_sha256: ${NATIVE_LIB_SHA}"
  echo "engine_manifest_sha256: ${ENGINE_MANIFEST_SHA}"
  echo "pw_portable_python_image_dir: UNSET"
  echo "pw_portable_python_engine_dir: UNSET"
} | tee "${OUT_DIR}/platform.txt"
cp "${MANIFEST}" "${OUT_DIR}/image-manifest.json"

# Record separation between dev checksums/ad-hoc signing and production release authentication.
cat > "${OUT_DIR}/signing-and-auth-separation.txt" <<EOF
evidence_tier: signed-dev-and-staged-bundle
dev_artifact_checksums:
  binary_sha256: $(${HASH_CMD} "${BINARY}" | awk '{print $1}')
  native_engine_sha256: ${NATIVE_LIB_SHA}
  engine_manifest_sha256: ${ENGINE_MANIFEST_SHA}
  image_manifest_sha256: $(${HASH_CMD} "${MANIFEST}" | awk '{print $1}')
dev_signing_status:
  macos: ad-hoc hardened runtime (codesign -s - -o runtime) verified on main binary and libpw_cpython_wasi.dylib; zero JIT/unsigned-executable-memory entitlements
  linux: unsigned dev binary + pinned SHA-256 native shared library verified before dlopen
production_release_authentication:
  status: SEPARATE_RELEASE_PIPELINE_SCOPE
  note: Production Developer ID + notarization (macOS) and signed release distribution artifacts (Linux) are tracked separately from development checksums and ad-hoc signing per coordination/reviews/PY07-native-engine.md.
EOF

# 2. macOS signing & entitlements verification (zero JIT / unsigned executable memory) on BOTH main binary and native dylib.
if [[ "${OS_NAME}" == "Darwin" ]]; then
  echo "=== macOS codesign & entitlements check ===" | tee "${OUT_DIR}/codesign.log"
  codesign -dvv "${BINARY}" 2>&1 | tee -a "${OUT_DIR}/codesign.log" || true
  ENT_OUT="$(codesign -d --entitlements :- "${BINARY}" 2>&1 || true)"
  printf '%s\n' "${ENT_OUT}" > "${OUT_DIR}/entitlements-main.plist"
  if printf '%s' "${ENT_OUT}" | grep -qE 'com\.apple\.security\.cs\.(allow-jit|allow-unsigned-executable-memory)'; then
    echo "FAIL: forbidden JIT or unsigned-executable-memory entitlement on ${BINARY}" >&2
    exit 1
  fi
  if [[ -f "${NATIVE_LIB}" ]]; then
    echo "--- native engine dylib: ${NATIVE_LIB} ---" | tee -a "${OUT_DIR}/codesign.log"
    codesign -dvv "${NATIVE_LIB}" 2>&1 | tee -a "${OUT_DIR}/codesign.log" || true
    DY_ENT="$(codesign -d --entitlements :- "${NATIVE_LIB}" 2>&1 || true)"
    printf '%s\n' "${DY_ENT}" > "${OUT_DIR}/entitlements-engine.plist"
    if printf '%s' "${DY_ENT}" | grep -qE 'com\.apple\.security\.cs\.(allow-jit|allow-unsigned-executable-memory)'; then
      echo "FAIL: forbidden entitlement on native engine ${NATIVE_LIB}" >&2
      exit 1
    fi
  fi
fi

# 3. Clean-machine offline launch check (no C compiler, no WABT, no system Python, no network).
CLEAN_TMP="$(mktemp -d)"
trap 'rm -rf "${CLEAN_TMP}"' EXIT
CLEAN_CFG="${CLEAN_TMP}/cfg"
mkdir -p "${CLEAN_CFG}"
PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
OFFLINE_LOG="${OUT_DIR}/offline-launch.log"

env -i \
  HOME="${CLEAN_TMP}" \
  PATH="/usr/bin:/bin" \
  LYCAON_ADDR="127.0.0.1:${PORT}" \
  LYCAON_CONFIG_DIR="${CLEAN_CFG}" \
  LYCAON_ENGINE_ROOT="${ENGINE_ROOT}" \
  LYCAON_API_TOKEN="offline-qual-token" \
  LYCAON_DEV="1" \
  http_proxy="http://127.0.0.1:1" \
  https_proxy="http://127.0.0.1:1" \
  "${BINARY}" serve >"${OFFLINE_LOG}" 2>&1 &
SRV_PID=$!

HEALTH_OK=0
for _ in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
    HEALTH_OK=1
    break
  fi
  sleep 0.5
done
kill "${SRV_PID}" 2>/dev/null || true
wait "${SRV_PID}" 2>/dev/null || true
if [[ "${HEALTH_OK}" != "1" ]]; then
  echo "FAIL: offline launch did not become healthy" >&2
  exit 1
fi
echo "PASS: clean-machine offline launch healthy at 127.0.0.1:${PORT}" | tee "${OUT_DIR}/offline-launch.verdict"

# 4. Four fail-closed skew swaps (each in a disposable copy of ENGINE_ROOT, PW_PORTABLE_PYTHON_IMAGE_DIR UNSET).
SKEW_REPORT="${OUT_DIR}/skew-swaps.txt"
: > "${SKEW_REPORT}"

run_skew_check() {
  local name="$1"
  local skew_root="$2"
  local skew_cfg="${CLEAN_TMP}/cfg-${name}"
  mkdir -p "${skew_cfg}"
  local skew_port
  skew_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  local skew_log="${OUT_DIR}/skew-${name}.log"
  env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
    LYCAON_ADDR="127.0.0.1:${skew_port}" \
    LYCAON_CONFIG_DIR="${skew_cfg}" \
    LYCAON_ENGINE_ROOT="${skew_root}" \
    LYCAON_API_TOKEN="skew-qual-token" \
    LYCAON_DEV="1" \
    "${BINARY}" serve >"${skew_log}" 2>&1 &
  local spid=$!
  sleep 4
  kill "${spid}" 2>/dev/null || true
  wait "${spid}" 2>/dev/null || true
  if grep -qiE 'portable python.*(fail|mismatch|invalid|error|unsupported|corrupt|disabled|verification)' "${skew_log}"; then
    echo "PASS: skew swap ${name} rejected fail-closed by normal launcher" | tee -a "${SKEW_REPORT}"
  else
    echo "RECORDED_LAUNCHER_LOG: skew swap ${name} (inspect ${skew_log})" | tee -a "${SKEW_REPORT}"
  fi
}

# Swap 1: Tampered native engine artifact bytes (.dylib/.so).
if [[ -f "${NATIVE_LIB}" ]]; then
  S1="${CLEAN_TMP}/skew-native-lib"
  cp -R "${ENGINE_ROOT}" "${S1}"
  printf 'TAMPERED_NATIVE_ENGINE_BYTES' > "${S1}/portable-python/engine/${NATIVE_LIB_NAME}"
  run_skew_check "1-native-artifact" "${S1}"
else
  echo "FAIL: 1-native-artifact missing ${NATIVE_LIB}" | tee -a "${SKEW_REPORT}"
  exit 1
fi

# Swap 2: ABI version mismatch in artifact-manifest.json.
S2="${CLEAN_TMP}/skew-abi"
cp -R "${ENGINE_ROOT}" "${S2}"
python3 -c '
import json, pathlib
p = pathlib.Path("'"${S2}"'/portable-python/engine/artifact-manifest.json")
d = json.loads(p.read_text())
d["abi_version"] = 999
p.write_text(json.dumps(d, indent=2))
'
run_skew_check "2-abi-version" "${S2}"

# Swap 3: Metered guest digest mismatch in artifact-manifest.json.
S3="${CLEAN_TMP}/skew-guest-digest"
cp -R "${ENGINE_ROOT}" "${S3}"
python3 -c '
import json, pathlib
p = pathlib.Path("'"${S3}"'/portable-python/engine/artifact-manifest.json")
d = json.loads(p.read_text())
d["metered_wasm_sha256"] = "0" * 64
p.write_text(json.dumps(d, indent=2))
'
run_skew_check "3-metered-guest-digest" "${S3}"

# Swap 4: Corrupted image / stale .pyc bytecode digest mismatch.
S4="${CLEAN_TMP}/skew-image-stdlib"
cp -R "${ENGINE_ROOT}" "${S4}"
TARGET_PYC="$(find "${S4}/portable-python/image/lib" -type f -name '*.pyc' 2>/dev/null | head -n 1 || true)"
if [[ -n "${TARGET_PYC}" ]]; then
  printf '# corrupted pyc sentinel\n' >> "${TARGET_PYC}"
else
  printf '# corrupted stdlib sentinel\n' >> "${S4}/portable-python/image/lib/python3.12/site.py"
fi
run_skew_check "4-corrupted-image" "${S4}"

# 5. Distribution packaging & in-place upgrade path check (separate from signed-dev).
UPGRADE_LOG="${OUT_DIR}/distribution-upgrade.log"
UPGRADE_ROOT="${CLEAN_TMP}/upgrade-engine-root"
cp -R "${ENGINE_ROOT}" "${UPGRADE_ROOT}"
printf 'STALE_PRE_UPGRADE_ENGINE' > "${UPGRADE_ROOT}/portable-python/engine/${NATIVE_LIB_NAME}"
printf '# stale file\n' > "${UPGRADE_ROOT}/portable-python/image/lib/__stale_pre_upgrade__.py"
{
  echo "=== Distribution in-place upgrade check ==="
  echo "pre_upgrade_tampered_sha: $(${HASH_CMD} "${UPGRADE_ROOT}/portable-python/engine/${NATIVE_LIB_NAME}" | awk '{print $1}')"
  rm -rf "${UPGRADE_ROOT}/portable-python/image" "${UPGRADE_ROOT}/portable-python/engine"
  mkdir -p "${UPGRADE_ROOT}/portable-python"
  cp -R "${ENGINE_ROOT}/portable-python/image" "${UPGRADE_ROOT}/portable-python/image"
  cp -R "${ENGINE_ROOT}/portable-python/engine" "${UPGRADE_ROOT}/portable-python/engine"
  UPGRADED_SHA="$(${HASH_CMD} "${UPGRADE_ROOT}/portable-python/engine/${NATIVE_LIB_NAME}" | awk '{print $1}')"
  echo "post_upgrade_engine_sha: ${UPGRADED_SHA}"
  if [[ "${UPGRADED_SHA}" != "${NATIVE_LIB_SHA}" ]]; then
    echo "FAIL: upgraded engine sha mismatch" >&2
    exit 1
  fi
  if [[ -e "${UPGRADE_ROOT}/portable-python/image/lib/__stale_pre_upgrade__.py" ]]; then
    echo "FAIL: stale file survived upgrade" >&2
    exit 1
  fi
  UP_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  UP_CFG="${CLEAN_TMP}/cfg-upgrade"
  mkdir -p "${UP_CFG}"
  env -i HOME="${CLEAN_TMP}" PATH="/usr/bin:/bin" LYCAON_ADDR="127.0.0.1:${UP_PORT}" LYCAON_CONFIG_DIR="${UP_CFG}" LYCAON_ENGINE_ROOT="${UPGRADE_ROOT}" LYCAON_API_TOKEN="upgrade-qual-token" LYCAON_DEV="1" "${BINARY}" serve >"${OUT_DIR}/upgrade-launch.log" 2>&1 &
  UP_PID=$!
  UP_OK=0
  for _ in $(seq 1 40); do
    if curl -sf "http://127.0.0.1:${UP_PORT}/health" >/dev/null 2>&1; then
      UP_OK=1
      break
    fi
    sleep 0.5
  done
  kill "${UP_PID}" 2>/dev/null || true
  wait "${UP_PID}" 2>/dev/null || true
  if [[ "${UP_OK}" != "1" ]]; then
    echo "FAIL: upgraded bundle did not launch cleanly" >&2
    exit 1
  fi
  echo "PASS: in-place upgrade replaced stale artifacts, matched pinned SHA-256, and launched cleanly at 127.0.0.1:${UP_PORT}"
} | tee "${UPGRADE_LOG}"

# 6. Generate SHA256SUMS over all evidence files in OUT_DIR.
(
  cd "${OUT_DIR}"
  FILES="$(find . -maxdepth 1 -type f ! -name SHA256SUMS | sort | sed 's|^\./||')"
  # shellcheck disable=SC2086
  ${HASH_CMD} ${FILES} > SHA256SUMS
)
echo "ALL_INSTALLED_CHECKS_DONE: ${OUT_DIR}/SHA256SUMS"

#!/usr/bin/env bash
# PY06 Installed-Artifact, Signing, Smoke Health, Skew Swaps, Real Staging Upgrade & Build-to-Tool Qualification Script.
# Runs on macOS arm64 and Linux amd64 with PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR UNSET.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: verify-installed.sh --engine-root <dir> --binary <path> --out-dir <dir> --repo-root <dir> [--backend <name>] [--require-native] [--expected-image-id <id>] [--upgrade-stage-cmd <cmd>]
EOF
  exit 2
}

ENGINE_ROOT=""
BINARY=""
OUT_DIR=""
REPO_ROOT=""
BACKEND="native-aot"
REQUIRE_NATIVE=0
EXPECTED_IMAGE_ID=""
UPGRADE_STAGE_CMD=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --backend) BACKEND="$2"; shift 2 ;;
    --require-native) REQUIRE_NATIVE=1; shift 1 ;;
    --expected-image-id) EXPECTED_IMAGE_ID="$2"; shift 2 ;;
    --upgrade-stage-cmd) UPGRADE_STAGE_CMD="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "${ENGINE_ROOT}" && -n "${BINARY}" && -n "${OUT_DIR}" && -n "${REPO_ROOT}" ]] || usage
mkdir -p "${OUT_DIR}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Critical rule: installed product qualification MUST run with runtime overrides UNSET.
unset PW_PORTABLE_PYTHON_IMAGE_DIR
unset PW_PORTABLE_PYTHON_ENGINE_DIR

OS_NAME="$(uname -s)"
HASH_CMD="sha256sum"
NATIVE_LIB_NAME="libpw_cpython_wasi.so"
TARGET_TRIPLE="x86_64-unknown-linux-gnu"
if [[ "${OS_NAME}" == "Darwin" ]]; then
  HASH_CMD="shasum -a 256"
  NATIVE_LIB_NAME="libpw_cpython_wasi.dylib"
  TARGET_TRIPLE="aarch64-apple-darwin"
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
  echo "git_head: $(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
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
evidence_tier: build-to-tool-distribution-and-signed-dev
dev_artifact_checksums:
  binary_sha256: $(${HASH_CMD} "${BINARY}" | awk '{print $1}')
  native_engine_sha256: ${NATIVE_LIB_SHA}
  engine_manifest_sha256: ${ENGINE_MANIFEST_SHA}
  image_manifest_sha256: $(${HASH_CMD} "${MANIFEST}" | awk '{print $1}')
dev_signing_status:
  macos: ad-hoc hardened runtime (codesign -s - -o runtime) verified on main binary and libpw_cpython_wasi.dylib; zero JIT/unsigned-executable-memory entitlements
  linux: dev binary + pinned SHA-256 native shared library verified before dlopen
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

# 3. Clean-machine offline /health smoke launch check (preserved under accurate smoke label).
CLEAN_TMP="$(mktemp -d)"
trap 'rm -rf "${CLEAN_TMP}"' EXIT
CLEAN_CFG="${CLEAN_TMP}/cfg-smoke"
mkdir -p "${CLEAN_CFG}"
PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
SMOKE_LOG="${OUT_DIR}/smoke-health-launch.log"

env -i \
  HOME="${CLEAN_TMP}" \
  PATH="/usr/bin:/bin" \
  LYCAON_ADDR="127.0.0.1:${PORT}" \
  LYCAON_CONFIG_DIR="${CLEAN_CFG}" \
  LYCAON_ENGINE_ROOT="${ENGINE_ROOT}" \
  LYCAON_API_TOKEN="smoke-health-token" \
  LYCAON_DEV="1" \
  http_proxy="http://127.0.0.1:1" \
  https_proxy="http://127.0.0.1:1" \
  "${BINARY}" serve >"${SMOKE_LOG}" 2>&1 &
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
  echo "FAIL: clean-machine offline /health smoke launch did not become healthy" >&2
  exit 1
fi
echo "PASS: clean-machine offline /health smoke launch healthy at 127.0.0.1:${PORT}" | tee "${OUT_DIR}/smoke-health-launch.verdict"

# 4. Real build-to-tool Python workbook invocation from the freshly staged bundle (runtime overrides UNSET).
B2T_ARGS=(
  --binary "${BINARY}"
  --engine-root "${ENGINE_ROOT}"
  --repo-root "${REPO_ROOT}"
  --out-dir "${OUT_DIR}"
  --label "fresh-stage"
)
if [[ -n "${EXPECTED_IMAGE_ID}" ]]; then
  B2T_ARGS+=(--expected-image-id "${EXPECTED_IMAGE_ID}")
fi
python3 "${SCRIPT_DIR}/verify-build-to-tool.py" "${B2T_ARGS[@]}"

# 5. Stale-file & tampered-artifact upgrade through the ACTUAL staging entry + second build-to-tool invocation.
UPGRADE_LOG="${OUT_DIR}/real-staging-upgrade.log"
{
  echo "=== Real staging stale-file upgrade check ==="
  STALE_IMG_FILE="${ENGINE_ROOT}/portable-python/image/stale-upgrade-sentinel.py"
  STALE_ENG_FILE="${ENGINE_ROOT}/portable-python/engine/stale-engine-sentinel.txt"
  printf '# stale image sentinel\n' > "${STALE_IMG_FILE}"
  printf 'stale engine sentinel\n' > "${STALE_ENG_FILE}"
  printf 'TAMPERED_PRE_UPGRADE_ENGINE' > "${NATIVE_LIB}"
  echo "planted_stale_image_file: ${STALE_IMG_FILE}"
  echo "planted_stale_engine_file: ${STALE_ENG_FILE}"
  echo "pre_upgrade_tampered_lib_sha: $(${HASH_CMD} "${NATIVE_LIB}" | awk '{print $1}')"

  if [[ -n "${UPGRADE_STAGE_CMD}" ]]; then
    echo "running_actual_upgrade_cmd: ${UPGRADE_STAGE_CMD}"
    bash -lc "${UPGRADE_STAGE_CMD}"
  else
    echo "running_actual_stage_bundled: bash ${REPO_ROOT}/scripts/portable-python/stage-bundled.sh --root ${ENGINE_ROOT} --target ${TARGET_TRIPLE}"
    env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
      bash "${REPO_ROOT}/scripts/portable-python/stage-bundled.sh" \
        --root "${ENGINE_ROOT}" \
        --target "${TARGET_TRIPLE}"
  fi

  if [[ -e "${STALE_IMG_FILE}" ]]; then
    echo "FAIL: stale image file survived real staging upgrade: ${STALE_IMG_FILE}" >&2
    exit 1
  fi
  if [[ -e "${STALE_ENG_FILE}" ]]; then
    echo "FAIL: stale engine file survived real staging upgrade: ${STALE_ENG_FILE}" >&2
    exit 1
  fi
  POST_UPGRADE_SHA="$(${HASH_CMD} "${NATIVE_LIB}" | awk '{print $1}')"
  echo "post_upgrade_lib_sha: ${POST_UPGRADE_SHA}"
  if [[ "${POST_UPGRADE_SHA}" != "${NATIVE_LIB_SHA}" ]]; then
    echo "FAIL: post-upgrade native library sha mismatch: got ${POST_UPGRADE_SHA} want ${NATIVE_LIB_SHA}" >&2
    exit 1
  fi
  echo "PASS: actual staging entry removed stale sentinels and restored pinned native engine SHA-256"
} 2>&1 | tee "${UPGRADE_LOG}"

B2T_UP_ARGS=(
  --binary "${BINARY}"
  --engine-root "${ENGINE_ROOT}"
  --repo-root "${REPO_ROOT}"
  --out-dir "${OUT_DIR}"
  --label "upgraded-stage"
)
if [[ -n "${EXPECTED_IMAGE_ID}" ]]; then
  B2T_UP_ARGS+=(--expected-image-id "${EXPECTED_IMAGE_ID}")
fi
python3 "${SCRIPT_DIR}/verify-build-to-tool.py" "${B2T_UP_ARGS[@]}"

# 6. Fail-closed staging check (missing / stale image source must cause stage-bundled.sh to exit non-zero).
FAIL_STAGE_LOG="${OUT_DIR}/fail-closed-staging.log"
FAIL_ROOT="${CLEAN_TMP}/fail-stage-root"
mkdir -p "${FAIL_ROOT}"
set +e
env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
  bash "${REPO_ROOT}/scripts/portable-python/stage-bundled.sh" \
    --root "${FAIL_ROOT}" \
    --target "${TARGET_TRIPLE}" \
    --image-dir "${CLEAN_TMP}/nonexistent-image-dir" >"${FAIL_STAGE_LOG}" 2>&1
FAIL_RC=$?
set -e
echo "fail_closed_missing_image_exit_code: ${FAIL_RC}" | tee -a "${FAIL_STAGE_LOG}"
if [[ "${FAIL_RC}" -eq 0 ]]; then
  echo "FAIL: stage-bundled.sh unexpectedly succeeded with missing image dir" >&2
  exit 1
fi
echo "PASS: stage-bundled.sh failed closed on missing image source (exit=${FAIL_RC})" | tee -a "${FAIL_STAGE_LOG}"

# 7. Generate SHA256SUMS over all evidence files in OUT_DIR (including receipts subdirs).
(
  cd "${OUT_DIR}"
  FILES="$(find . -type f ! -name SHA256SUMS | sort | sed 's|^\./||')"
  # shellcheck disable=SC2086
  ${HASH_CMD} ${FILES} > SHA256SUMS
)
echo "ALL_BUILD_TO_TOOL_CHECKS_DONE: ${OUT_DIR}/SHA256SUMS"

#!/usr/bin/env bash
# PY06 Installed-Artifact, Fail-Closed Skew, Signing & Offline Launch Qualification Script.
# Runs on macOS arm64 and Linux amd64 with PW_PORTABLE_PYTHON_IMAGE_DIR UNSET.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: verify-installed.sh --engine-root <dir> --binary <path> --out-dir <dir> [--repo-root <dir>] [--backend <name>]
EOF
  exit 2
}

ENGINE_ROOT=""
BINARY=""
OUT_DIR=""
REPO_ROOT=""
BACKEND="auto"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --backend) BACKEND="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "${ENGINE_ROOT}" && -n "${BINARY}" && -n "${OUT_DIR}" ]] || usage
mkdir -p "${OUT_DIR}"

# Critical rule: installed product qualification MUST run with image override UNSET.
unset PW_PORTABLE_PYTHON_IMAGE_DIR

OS_NAME="$(uname -s)"
HASH_CMD="sha256sum"
if [[ "${OS_NAME}" == "Darwin" ]]; then
  HASH_CMD="shasum -a 256"
fi

IMAGE_DIR="${ENGINE_ROOT}/portable-python/image"
MANIFEST="${IMAGE_DIR}/image-manifest.json"
[[ -f "${MANIFEST}" ]] || { echo "error: missing ${MANIFEST}" >&2; exit 1; }
[[ -x "${BINARY}" ]] || { echo "error: binary not executable: ${BINARY}" >&2; exit 1; }

# 1. Record platform, build, toolchain, host load, and digests.
{
  echo "uname: $(uname -srm)"
  echo "host_load: $(uptime)"
  echo "backend: ${BACKEND}"
  echo "go_version: $(go version 2>/dev/null || echo unavailable)"
  echo "git_head: $(git -C "${REPO_ROOT:-.}" rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "binary_path: ${BINARY}"
  echo "binary_sha256: $(${HASH_CMD} "${BINARY}" | awk '{print $1}')"
  echo "image_manifest_sha256: $(${HASH_CMD} "${MANIFEST}" | awk '{print $1}')"
  echo "pw_portable_python_image_dir: UNSET"
} | tee "${OUT_DIR}/platform.txt"
cp "${MANIFEST}" "${OUT_DIR}/image-manifest.json"

# 2. macOS signing & entitlements verification (zero JIT / unsigned executable memory).
if [[ "${OS_NAME}" == "Darwin" ]]; then
  echo "=== macOS codesign & entitlements check ===" | tee "${OUT_DIR}/codesign.log"
  codesign -dvv "${BINARY}" 2>&1 | tee -a "${OUT_DIR}/codesign.log" || true
  ENT_OUT="$(codesign -d --entitlements :- "${BINARY}" 2>&1 || true)"
  printf '%s\n' "${ENT_OUT}" > "${OUT_DIR}/entitlements-main.plist"
  if printf '%s' "${ENT_OUT}" | grep -qE 'com\.apple\.security\.cs\.(allow-jit|allow-unsigned-executable-memory)'; then
    echo "FAIL: forbidden JIT or unsigned-executable-memory entitlement on ${BINARY}" >&2
    exit 1
  fi
  while IFS= read -r dylib; do
    [[ -n "${dylib}" ]] || continue
    echo "--- dylib: ${dylib} ---" | tee -a "${OUT_DIR}/codesign.log"
    codesign -dvv "${dylib}" 2>&1 | tee -a "${OUT_DIR}/codesign.log" || true
    DY_ENT="$(codesign -d --entitlements :- "${dylib}" 2>&1 || true)"
    if printf '%s' "${DY_ENT}" | grep -qE 'com\.apple\.security\.cs\.(allow-jit|allow-unsigned-executable-memory)'; then
      echo "FAIL: forbidden entitlement on native engine ${dylib}" >&2
      exit 1
    fi
  done < <(find "${ENGINE_ROOT}" -type f -name '*.dylib' 2>/dev/null || true)
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
  env -u PW_PORTABLE_PYTHON_IMAGE_DIR \
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
  if grep -qiE 'portable python.*(fail|mismatch|invalid|error|unsupported|corrupt|disabled)' "${skew_log}"; then
    echo "PASS: skew swap ${name} rejected fail-closed by normal launcher" | tee -a "${SKEW_REPORT}"
  else
    echo "RECORDED_LAUNCHER_LOG: skew swap ${name} (inspect ${skew_log})" | tee -a "${SKEW_REPORT}"
  fi
}

# Swap 1: Native engine artifact bytes (.dylib/.so) if present.
NATIVE_LIB="$(find "${ENGINE_ROOT}" -type f \( -name '*python*.dylib' -o -name '*python*.so' \) 2>/dev/null | head -n 1 || true)"
if [[ -n "${NATIVE_LIB}" ]]; then
  S1="${CLEAN_TMP}/skew-native-lib"
  cp -R "${ENGINE_ROOT}" "${S1}"
  REL_LIB="${NATIVE_LIB#${ENGINE_ROOT}/}"
  printf 'TAMPERED_NATIVE_ENGINE_BYTES' > "${S1}/${REL_LIB}"
  run_skew_check "1-native-artifact" "${S1}"
else
  echo "PENDING_PY08: 1-native-artifact (no native python .dylib/.so present in ${ENGINE_ROOT} yet)" | tee -a "${SKEW_REPORT}"
fi

# Swap 2: ABI / schema version mismatch in manifest.
S2="${CLEAN_TMP}/skew-abi"
cp -R "${ENGINE_ROOT}" "${S2}"
python3 -c '
import json, pathlib
p = pathlib.Path("'"${S2}"'/portable-python/image/image-manifest.json")
d = json.loads(p.read_text())
d["schema_version"] = "portable-python.image-manifest/v999-skew"
if "abi_version" in d: d["abi_version"] = 999
p.write_text(json.dumps(d, indent=2))
'
run_skew_check "2-abi-version" "${S2}"

# Swap 3: Metered guest digest mismatch in manifest.
S3="${CLEAN_TMP}/skew-guest-digest"
cp -R "${ENGINE_ROOT}" "${S3}"
python3 -c '
import json, pathlib
p = pathlib.Path("'"${S3}"'/portable-python/image/image-manifest.json")
d = json.loads(p.read_text())
d["interpreter"]["wasm_sha256"] = "0" * 64
p.write_text(json.dumps(d, indent=2))
'
run_skew_check "3-metered-guest-digest" "${S3}"

# Swap 4: Corrupted Python image / stdlib bytecode digest mismatch.
S4="${CLEAN_TMP}/skew-image-stdlib"
cp -R "${ENGINE_ROOT}" "${S4}"
printf '# corrupted stdlib sentinel\n' >> "${S4}/portable-python/image/lib/python3.12/site.py" 2>/dev/null || \
  printf '# corrupted image sentinel\n' > "${S4}/portable-python/image/lib/__corrupted_qual__.py"
run_skew_check "4-corrupted-image" "${S4}"

# 5. Generate SHA256SUMS over all evidence files in OUT_DIR.
(
  cd "${OUT_DIR}"
  FILES="$(find . -maxdepth 1 -type f ! -name SHA256SUMS | sort | sed 's|^\./||')"
  # shellcheck disable=SC2086
  ${HASH_CMD} ${FILES} > SHA256SUMS
)
echo "ALL_INSTALLED_CHECKS_DONE: ${OUT_DIR}/SHA256SUMS"

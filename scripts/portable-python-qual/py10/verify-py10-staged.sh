#!/usr/bin/env bash
# PY10 Staged-App Qualification, Stale-Pair Refusal, Independent Host PDF Render & Regression Suite.
# Runs on macOS arm64 and Linux amd64 with PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR UNSET.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: verify-py10-staged.sh --engine-root <dir> --out-dir <dir> --repo-root <dir> [--old-py08-image <dir>] [--old-py08-engine <dir>] [--image-archive <tar.gz>] [--engine-archive <tar.gz>] [--run-regressions]
EOF
  exit 2
}

ENGINE_ROOT=""
OUT_DIR=""
REPO_ROOT=""
OLD_IMAGE=""
OLD_ENGINE=""
IMAGE_ARCHIVE=""
ENGINE_ARCHIVE=""
RUN_REGRESSIONS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --engine-root) ENGINE_ROOT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --old-py08-image) OLD_IMAGE="$2"; shift 2 ;;
    --old-py08-engine) OLD_ENGINE="$2"; shift 2 ;;
    --image-archive) IMAGE_ARCHIVE="$2"; shift 2 ;;
    --engine-archive) ENGINE_ARCHIVE="$2"; shift 2 ;;
    --run-regressions) RUN_REGRESSIONS=1; shift 1 ;;
    *) usage ;;
  esac
done

[[ -n "${ENGINE_ROOT}" && -n "${OUT_DIR}" && -n "${REPO_ROOT}" ]] || usage
mkdir -p "${OUT_DIR}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

unset PW_PORTABLE_PYTHON_IMAGE_DIR
unset PW_PORTABLE_PYTHON_ENGINE_DIR

OS_NAME="$(uname -s)"
HASH_CMD="sha256sum"
TARGET_TRIPLE="x86_64-unknown-linux-gnu"
if [[ "${OS_NAME}" == "Darwin" ]]; then
  HASH_CMD="shasum -a 256"
  TARGET_TRIPLE="aarch64-apple-darwin"
fi

# 1. Stage real PY10 bundle from repo checkout with runtime overrides UNSET.
env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
  bash "${REPO_ROOT}/scripts/portable-python/stage-bundled.sh" \
    --root "${ENGINE_ROOT}" \
    --target "${TARGET_TRIPLE}" \
    --require-engine \
    --require-image 2>&1 | tee "${OUT_DIR}/stage-py10-bundle.log"

# 2. Verify bidirectional stale-pair refusal + host extension binary refusal (fail-closed before guest execution).
(
  cd "${REPO_ROOT}/lycaon"
  env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
    PW_QUAL_ENGINE_ROOT="${ENGINE_ROOT}" \
    PW_PY10_STALE_REFUSAL=1 \
    PW_PY10_OLD_IMAGE_DIR="${OLD_IMAGE}" \
    PW_PY10_OLD_ENGINE_DIR="${OLD_ENGINE}" \
    go test -v -count=1 -timeout 120s -run '^TestPY10_StalePairAndHostExtensionRefusal$' ./internal/portablepython \
    2>&1 | tee "${OUT_DIR}/stale-pair-refusal.log"
)

# 3. Run 5-run fresh-guest timing & size matrix (also emits py10-office-report.pdf).
PERF_ARGS=(
  --engine-root "${ENGINE_ROOT}"
  --out-dir "${OUT_DIR}"
  --repo-root "${REPO_ROOT}"
)
if [[ -n "${IMAGE_ARCHIVE}" ]]; then PERF_ARGS+=(--image-archive "${IMAGE_ARCHIVE}"); fi
if [[ -n "${ENGINE_ARCHIVE}" ]]; then PERF_ARGS+=(--engine-archive "${ENGINE_ARCHIVE}"); fi
bash "${SCRIPT_DIR}/run-py10-perf.sh" "${PERF_ARGS[@]}"

# 4. Run independent host PDF render & margin/clipping verification on the generated 2-page PDF.
python3 "${SCRIPT_DIR}/verify-pdf-render.py" \
  --pdf "${OUT_DIR}/py10-office-report.pdf" \
  --out-dir "${OUT_DIR}/pdf-render" \
  --expected-pages 2 2>&1 | tee "${OUT_DIR}/pdf-render-check.log"

# 5. Optionally run affected regression suites & budgets on the rebuilt interpreter.
if [[ "${RUN_REGRESSIONS}" == "1" ]]; then
  (
    cd "${REPO_ROOT}"
    env -u PW_PORTABLE_PYTHON_IMAGE_DIR -u PW_PORTABLE_PYTHON_ENGINE_DIR \
      PW_QUAL_ENGINE_ROOT="${ENGINE_ROOT}" \
      PW_REQUIRE_NATIVE_QUAL=1 \
      ./task test:digest -- \
        ./internal/pythonruntime/... \
        ./internal/portablepython/... \
        ./test/contract/portablepython/... \
        ./test/contract/invariants/... \
      2>&1 | tee "${OUT_DIR}/py10-regressions-digest.log"
    ./task budgets 2>&1 | tee "${OUT_DIR}/py10-budgets.log"
  )
fi

# 6. Generate SHA256SUMS over all evidence files.
(
  cd "${OUT_DIR}"
  FILES="$(find . -type f ! -name SHA256SUMS | sort | sed 's|^\./||')"
  # shellcheck disable=SC2086
  ${HASH_CMD} ${FILES} > SHA256SUMS
)
echo "PY10_STAGED_QUAL_DONE: ${OUT_DIR}/SHA256SUMS"

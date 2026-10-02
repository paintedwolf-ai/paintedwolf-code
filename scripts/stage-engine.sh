#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_DIR="${ROOT}/lycaon"
TAURI_DIR="${ROOT}/lycaon-den/src-tauri"
BINARIES_DIR="${TAURI_DIR}/binaries"
ENGINE_ROOT="${TAURI_DIR}/engine-root"
SIDECAR_NAME="pw"
LOGS_NAME="pw-logs"
DECIDE_NAME="bialy"
DOCUMENT_CORE_NAME="pw-document-core"

MODE="full"
RELEASE_BUILD=0
for arg in "$@"; do
  case "${arg}" in
    --minimal) MODE="minimal" ;;
    --release) RELEASE_BUILD=1 ;;
    *)
      echo "usage: stage-engine.sh [--minimal] [--release]" >&2
      echo "error: unknown argument ${arg}" >&2
      exit 2
      ;;
  esac
done

if [[ "${RELEASE_BUILD}" == "1" && -n "${LYCAON_OPENGREP_CANDIDATE:-}" ]]; then
  echo "error: local Opengrep candidates cannot be used in release packaging" >&2
  exit 2
fi

if ! command -v go >/dev/null 2>&1; then
  echo "error: go required — see lycaon/go.mod" >&2
  exit 1
fi
if ! command -v rustc >/dev/null 2>&1; then
  echo "error: rustc required (external binaries are named per host triple) — install Rust from https://rustup.rs" >&2
  exit 1
fi

TARGET="$(rustc --print host-tuple)"
HOST_KIND=""
EXE_SUFFIX=""
case "${TARGET}" in
  *-apple-darwin) HOST_KIND="darwin" ;;
  *-unknown-linux-gnu) HOST_KIND="linux" ;;
  *-pc-windows-msvc) HOST_KIND="windows"; EXE_SUFFIX=".exe" ;;
  *) echo "error: unsupported desktop target ${TARGET}" >&2; exit 1 ;;
esac
SIDECAR_BIN="${BINARIES_DIR}/${SIDECAR_NAME}-${TARGET}${EXE_SUFFIX}"
LOGS_BIN="${BINARIES_DIR}/${LOGS_NAME}-${TARGET}${EXE_SUFFIX}"
DECIDE_BIN="${BINARIES_DIR}/${DECIDE_NAME}-${TARGET}${EXE_SUFFIX}"
DOCUMENT_CORE_BIN="${BINARIES_DIR}/${DOCUMENT_CORE_NAME}-${TARGET}${EXE_SUFFIX}"

OPENGREP_ARTIFACT="$(bash "${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only)"
OPENGREP_IDENTITY="$(cd "${GO_DIR}" && env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode identity -artifact-directory "${OPENGREP_ARTIFACT}")"

mkdir -p "${BINARIES_DIR}"
if [[ "${MODE}" == "full" ]]; then
  rm -rf "${ENGINE_ROOT}"
fi
mkdir -p "${ENGINE_ROOT}"

rm -rf "${ENGINE_ROOT}/schemas"
mkdir -p "${ENGINE_ROOT}/schemas"
cp -R "${ROOT}/schemas/." "${ENGINE_ROOT}/schemas/"

bash "${ROOT}/scripts/stage-bundled-opengrep.sh" "${ENGINE_ROOT}" "${TARGET}" "${OPENGREP_ARTIFACT}"
if [[ "${MODE}" == "full" ]]; then
  bash "${ROOT}/scripts/gitengine-fetch.sh"
fi

VERSION_FILE="${ROOT}/VERSION"
if [[ ! -f "${VERSION_FILE}" ]]; then
  echo "error: missing ${VERSION_FILE}" >&2
  exit 1
fi
APP_VERSION="$(tr -d '[:space:]' < "${VERSION_FILE}")"

if [[ "${HOST_KIND}" == "darwin" ]]; then
  # Both native binaries share one deployment floor.
  MACOS_FLOOR_FILE="${GO_DIR}/internal/platformfloor/macos_floor.txt"
  if [[ ! -f "${MACOS_FLOOR_FILE}" ]]; then
    echo "error: missing ${MACOS_FLOOR_FILE}" >&2
    exit 1
  fi
  export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < "${MACOS_FLOOR_FILE}")"
  # Native linking records the deployment floor in the sidecar binary.
  export CGO_ENABLED=1
fi

VERSION_PKG="github.com/lycaon/lycaon/internal/version"
LDFLAGS="-s -w -X ${VERSION_PKG}.Version=${APP_VERSION} -X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${OPENGREP_IDENTITY}"

echo "stage-engine — building Go sidecar + logs sibling for ${TARGET} (version ${APP_VERSION}, mode ${MODE})" >&2
(
  cd "${GO_DIR}"
  # Path trimming removes build-machine paths from packaged executables.
  BUILD_ARGS=(-trimpath -ldflags="${LDFLAGS}")
  if [[ "${RELEASE_BUILD}" == "1" ]]; then
    BUILD_ARGS+=(-tags=paintedwolf_release)
  fi
  go build "${BUILD_ARGS[@]}" -o "${SIDECAR_BIN}" ./cmd/lycaon
  go build "${BUILD_ARGS[@]}" -o "${LOGS_BIN}" ./cmd/pw-logs
)
chmod +x "${SIDECAR_BIN}" "${LOGS_BIN}"

echo "stage-engine — building the document core for ${TARGET}" >&2
bash "${ROOT}/scripts/build-document-core.sh" --output "${DOCUMENT_CORE_BIN}" >/dev/null

echo "stage-engine — building the decision engine for ${TARGET}" >&2
bash "${ROOT}/scripts/build-decide.sh" "${DECIDE_BIN}"
bash "${ROOT}/scripts/stage-decide-heads.sh" "${ENGINE_ROOT}"
# Cached downloads are private to the build user; an installed app may run as another.
chmod -R u+rwX,go+rX "${ENGINE_ROOT}/decide/heads"
# MLX's kernels ship as a resource the host hands to the engine (--metallib).
rm -f "${ENGINE_ROOT}/decide/mlx.metallib"
if [[ -f "$(dirname "${DECIDE_BIN}")/mlx.metallib" ]]; then
  cp "$(dirname "${DECIDE_BIN}")/mlx.metallib" "${ENGINE_ROOT}/decide/mlx.metallib"
fi

if [[ "${HOST_KIND}" == "darwin" ]]; then
  HOST_ARGS=(--binary "${SIDECAR_BIN}" --output "${TAURI_DIR}/host-bundle" --version "${APP_VERSION}")
  if [[ "${RELEASE_BUILD}" == "1" ]]; then
    HOST_ARGS+=(--release)
  fi
  python3 "${ROOT}/scripts/stage-macos-host.py" "${HOST_ARGS[@]}"
fi

if [[ "${MODE}" == "full" ]]; then
  echo "stage-engine — provisioning chrome-headless-shell into engine-root/browser" >&2
  BROWSER_OUT="$("${SIDECAR_BIN}" browser ensure)"
  BROWSER_PATH="$(printf '%s\n' "${BROWSER_OUT}" | sed -n 's/^browser ready .* path=//p')"
  if [[ "${HOST_KIND}" == "windows" ]] && command -v cygpath >/dev/null 2>&1; then
    BROWSER_PATH="$(cygpath -u "${BROWSER_PATH}")"
  fi
  if [[ -z "${BROWSER_PATH}" || ! -x "${BROWSER_PATH}" ]]; then
    echo "error: browser ensure failed: ${BROWSER_OUT}" >&2
    exit 1
  fi
  BROWSER_DIR="$(dirname "${BROWSER_PATH}")"
  rm -rf "${ENGINE_ROOT}/browser"
  mkdir -p "${ENGINE_ROOT}/browser"
  cp -R "${BROWSER_DIR}/." "${ENGINE_ROOT}/browser/"
  rm -f "${ENGINE_ROOT}/browser/.complete"
  chmod +x "${ENGINE_ROOT}/browser/$(basename "${BROWSER_PATH}")"

  # The host reads the completion marker, so it ships with the checkpoint.
  echo "stage-engine — provisioning the decision checkpoint into engine-root/decide/models" >&2
  MODEL_OUT="$("${SIDECAR_BIN}" decide ensure)"
  MODEL_PATH="$(printf '%s\n' "${MODEL_OUT}" | sed -n 's/^decision model ready .* path=//p')"
  if [[ "${HOST_KIND}" == "windows" ]] && command -v cygpath >/dev/null 2>&1; then
    MODEL_PATH="$(cygpath -u "${MODEL_PATH}")"
  fi
  if [[ -z "${MODEL_PATH}" || ! -f "${MODEL_PATH}/.complete" ]]; then
    echo "error: decide ensure failed: ${MODEL_OUT}" >&2
    exit 1
  fi
  rm -rf "${ENGINE_ROOT}/decide/models"
  mkdir -p "${ENGINE_ROOT}/decide/models"
  cp -cR "${MODEL_PATH}" "${ENGINE_ROOT}/decide/models/" 2>/dev/null ||
    cp -R "${MODEL_PATH}" "${ENGINE_ROOT}/decide/models/"
  chmod -R u+rwX,go+rX "${ENGINE_ROOT}/decide/models"
fi

echo "stage-engine — staged ${SIDECAR_BIN#${ROOT}/}, ${LOGS_BIN#${ROOT}/}, ${DOCUMENT_CORE_BIN#${ROOT}/}, ${DECIDE_BIN#${ROOT}/}, and ${ENGINE_ROOT#${ROOT}/} (${MODE})" >&2

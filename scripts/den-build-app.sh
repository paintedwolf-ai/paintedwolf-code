#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${LYCAON_OPENGREP_CANDIDATE:-}" ]]; then
  echo "error: local Opengrep candidates cannot be used in release packaging" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEN_DIR="${ROOT}/lycaon-den"
TAURI_DIR="${DEN_DIR}/src-tauri"

DEVTOOLS=1
OPEN_APP=0
APP_BUILD_PROFILE=release
for arg in "$@"; do
  case "${arg}" in
    --no-devtools) DEVTOOLS=0 ;;
    --open) OPEN_APP=1 ;;
    --debug) APP_BUILD_PROFILE=debug ;;
    *)
      echo "usage: den:app [-- --no-devtools] [--open] [--debug]" >&2
      echo "error: unknown argument ${arg}" >&2
      exit 2
      ;;
  esac
done

for tool in bun go cargo rustc; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "error: ${tool} required" >&2
    exit 1
  fi
done

TARGET="$(rustc --print host-tuple)"

bash "${ROOT}/scripts/sync-den-versions.sh"

if [[ "${APP_BUILD_PROFILE}" == "debug" ]]; then
  bash "${ROOT}/scripts/stage-engine.sh"
else
  bash "${ROOT}/scripts/stage-engine.sh" --release
fi
bash "${ROOT}/scripts/licenses-notices.sh"

BUILD_CONFIG='{"bundle":{"createUpdaterArtifacts":false}}'
BUILD_ARGS=(
  --bundles app
  --target "${TARGET}"
  --config "${BUILD_CONFIG}"
)
if [[ "${APP_BUILD_PROFILE}" == "debug" ]]; then
  BUILD_ARGS+=(--debug)
fi
if [[ "${DEVTOOLS}" == "1" ]]; then
  BUILD_ARGS+=(--features profiling)
  echo "den:app — tauri build (${APP_BUILD_PROFILE} app bundle, target ${TARGET}, Web Inspector ON)" >&2
else
  echo "den:app — tauri build (${APP_BUILD_PROFILE} app bundle, target ${TARGET}, Web Inspector off)" >&2
fi

(
  cd "${DEN_DIR}"
  if [[ "$(uname -s)" == "Darwin" ]]; then
    # A fresh asset compiler process avoids shared service state.
    export IBToolNeverDeque=1
    export PATH="${ROOT}/scripts/macos-build-tools:${PATH}"
  fi
  bun run tauri build "${BUILD_ARGS[@]}"
)

APP_PATH=""
if [[ "$(uname -s)" == "Darwin" ]]; then
  APP_PATH="$(find "${TAURI_DIR}/target/${TARGET}/${APP_BUILD_PROFILE}/bundle/macos" \
    -maxdepth 1 -name '*.app' -print -quit 2>/dev/null || true)"
fi

if [[ -z "${APP_PATH}" ]]; then
  echo "den:app — build finished; see ${TAURI_DIR#"${ROOT}/"}/target/${TARGET}/${APP_BUILD_PROFILE}/bundle/" >&2
  exit 0
fi

echo "den:app — built ${APP_PATH}" >&2
if [[ "${DEVTOOLS}" == "1" ]]; then
  echo "den:app — inspect: Safari > Settings > Advanced > 'Show features for web developers'," >&2
  echo "          then Develop > (this Mac) > Painted Wolf Code. Timeline is the Timelines tab." >&2
fi

if [[ "${OPEN_APP}" == "1" ]]; then
  open "${APP_PATH}"
fi

#!/usr/bin/env bash
# Build development executables with the maintained scanner identity.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
ARTIFACT_DIR="$(bash "${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only)"
cd "${ROOT}/lycaon"
IDENTITY="$(env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode identity -artifact-directory "${ARTIFACT_DIR}")"
env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode stage -artifact-directory "${ARTIFACT_DIR}" -root "${ROOT}/lycaon-den/src-tauri/engine-root" >/dev/null
APP_VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
LDFLAGS="-X github.com/lycaon/lycaon/internal/version.Version=${APP_VERSION} -X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${IDENTITY}"
mkdir -p "${PW_BUILD_DIR}"
go build -ldflags="${LDFLAGS}" -o "${PW_BUILD_DIR}/lycaon-dev" ./cmd/lycaon
go build -ldflags="${LDFLAGS}" -o "${PW_BUILD_DIR}/pw-logs" ./cmd/pw-logs
bash "${ROOT}/scripts/sign-dev-binary.sh" "${PW_BUILD_DIR}/lycaon-dev"
bash "${ROOT}/scripts/sign-dev-binary.sh" "${PW_BUILD_DIR}/pw-logs"
bash "${ROOT}/scripts/build-document-core.sh" --output "${PW_BUILD_DIR}/pw-document-core" >/dev/null
bash "${ROOT}/scripts/sign-dev-binary.sh" "${PW_BUILD_DIR}/pw-document-core"
bash "${ROOT}/scripts/build-decide.sh" "${PW_BUILD_DIR}/bialy"
bash "${ROOT}/scripts/sign-dev-binary.sh" "${PW_BUILD_DIR}/bialy"
bash "${ROOT}/scripts/stage-decide-heads.sh" "${ROOT}/lycaon-den/src-tauri/engine-root"
rm -f "${ROOT}/lycaon-den/src-tauri/engine-root/decide/mlx.metallib"
if [[ -f "${PW_BUILD_DIR}/mlx.metallib" ]]; then
  cp "${PW_BUILD_DIR}/mlx.metallib" "${ROOT}/lycaon-den/src-tauri/engine-root/decide/mlx.metallib"
fi

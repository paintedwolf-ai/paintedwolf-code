#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_DIR="${ROOT}/lycaon"

bash "${ROOT}/scripts/gitengine-ensure.sh"

ARTIFACT_DIR="$(bash "${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only)"
IDENTITY="$(cd "${GO_DIR}" && env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode identity -artifact-directory "${ARTIFACT_DIR}")"
(cd "${GO_DIR}" && env -u LYCAON_OPENGREP_CANDIDATE go run ./cmd/opengrep-artifact -mode stage -artifact-directory "${ARTIFACT_DIR}" -root "${ROOT}/lycaon-den/src-tauri/engine-root" >/dev/null)
APP_VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
LDFLAGS="-X github.com/lycaon/lycaon/internal/version.Version=${APP_VERSION} -X github.com/lycaon/lycaon/internal/scan/bundled.buildIdentityBase64=${IDENTITY}"

BIN_DIR="${LYCAON_E2E_STATE_DIR:?LYCAON_E2E_STATE_DIR is required}/runtime"
BIN="${BIN_DIR}/sidecar"
mkdir -p "${BIN_DIR}"
TMP_BIN="$(mktemp "${BIN_DIR}/sidecar.XXXXXX")"
trap 'rm -f "${TMP_BIN}"' EXIT
(cd "${GO_DIR}" && go build -ldflags="${LDFLAGS}" -o "${TMP_BIN}" ./cmd/lycaon)
chmod +x "${TMP_BIN}"

# Each harness keeps its own executable.
mv -f "${TMP_BIN}" "${BIN}"
trap - EXIT

# The engine runs its native document core from beside its own executable.
bash "${ROOT}/scripts/build-document-core.sh" --output "${BIN_DIR}/pw-document-core" >/dev/null
bash "${ROOT}/scripts/sign-dev-binary.sh" "${BIN_DIR}/pw-document-core"

# Provision once in the device cache; retain the resolved artifact for isolated runs.
BROWSER_MANIFEST="${BIN_DIR}/browser.json"
BROWSER_PENDING="$(mktemp "${BIN_DIR}/browser.XXXXXX")"
trap 'rm -f "${BROWSER_PENDING}"' EXIT
LYCAON_DEV=1 "${BIN}" browser ensure --json >"${BROWSER_PENDING}"
mv -f "${BROWSER_PENDING}" "${BROWSER_MANIFEST}"
trap - EXIT

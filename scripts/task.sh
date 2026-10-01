#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"

GO_DIR="${ROOT}/lycaon"
export GOTOOLCHAIN="go$(grep '^go ' "${GO_DIR}/go.mod" | awk '{print $2}')"
TASKFILE="${ROOT}/Taskfile.yml"
BIN_DIR="${PW_BIN_DIR}"
TASK_BIN="${BIN_DIR}/task"
TASK_VERSION="v3.51.1"
# Assignment preserves a failed toolchain lookup's exit status.
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="${BIN_DIR}:${GOPATH_BIN}:${PATH}"

if [[ ! -f "${TASKFILE}" ]]; then
  echo "error: Taskfile not found: ${TASKFILE}" >&2
  exit 1
fi

if ! "${TASK_BIN}" --version 2>/dev/null | grep -qF "${TASK_VERSION#v}"; then
  echo "Installing task ${TASK_VERSION} to ${BIN_DIR}..."
  mkdir -p "${BIN_DIR}"
  GOBIN="${BIN_DIR}" go install "github.com/go-task/task/v3/cmd/task@${TASK_VERSION}"
fi

exec python3 "${ROOT}/scripts/test-execution.py" task -- "${TASK_BIN}" "${ROOT}" "$@"

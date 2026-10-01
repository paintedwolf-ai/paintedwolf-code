#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGED="${ROOT}/lycaon-den/src-tauri/engine-root/gitengine"

TARGET="$(rustc --print host-tuple)"
case "${TARGET}" in
  x86_64-pc-windows-msvc) GIT_REL="cmd/git.exe" ;;
  aarch64-apple-darwin|x86_64-unknown-linux-gnu|aarch64-unknown-linux-gnu) GIT_REL="bin/git" ;;
  *) echo "error: gitengine:ensure does not support ${TARGET}" >&2; exit 1 ;;
esac

bash "${ROOT}/scripts/gitengine-fetch.sh"

if [[ ! -f "${STAGED}/${GIT_REL}" ]]; then
  echo "error: gitengine:ensure — no staged toolchain at ${STAGED#"${ROOT}"/}" >&2
  exit 1
fi

echo "gitengine:ensure — staged ${STAGED#"${ROOT}"/}" >&2

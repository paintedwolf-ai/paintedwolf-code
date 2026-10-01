#!/usr/bin/env bash
# Prepare the versioned upgrade fixture.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
OUT="${ROOT}/lycaon/testdata/upgrade-corpus/${VERSION}"

if [[ $# -ne 0 ]]; then
  echo "usage: upgrade-corpus-prepare.sh" >&2
  exit 2
fi
if [[ -e "${OUT}" || -L "${OUT}" ]]; then
  echo "error: output already exists: ${OUT}" >&2
  exit 1
fi

# shellcheck source=scripts/artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
mkdir -p "$(dirname "${OUT}")"
STAGING="$(mktemp -d "$(dirname "${OUT}")/.prepare.XXXXXX")"
trap 'rm -rf "${STAGING}"' EXIT
bash "${ROOT}/scripts/upgrade-corpus-seed.sh" --out "${STAGING}/corpus" --sidecar "${PW_BUILD_DIR}/lycaon-dev"
python3 - "${VERSION}" "${STAGING}/corpus" "${OUT}" <<'PYCODE'
import json
import os
from pathlib import Path
import sys
version, source, destination = sys.argv[1], Path(sys.argv[2]), Path(sys.argv[3])
manifest = json.loads((source / 'MANIFEST.json').read_text())
if manifest.get('app_version') != version:
    raise SystemExit(f"corpus app_version={manifest.get('app_version')}; want {version}")
if destination.exists() or destination.is_symlink():
    raise SystemExit(f"output already exists: {destination}")
os.rename(source, destination)
PYCODE
echo "prepared versioned release corpus: ${OUT}" >&2

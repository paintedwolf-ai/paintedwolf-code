#!/usr/bin/env bash
# Register the hostile e2e fixture as a second project on a running harness.
# Requires LYCAON_E2E_API_URL + LYCAON_E2E_TOKEN (set by den:harness).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPTS="$(cd "${DIR}/.." && pwd)"
ROOT="$(cd "${SCRIPTS}/.." && pwd)"

# shellcheck source=scripts/e2e/env.sh
source "${SCRIPTS}/e2e/env.sh"

STATE_DIR="${LYCAON_E2E_STATE_DIR:-}"
if [[ -z "${STATE_DIR}" ]]; then
  echo "error: LYCAON_E2E_STATE_DIR required" >&2
  exit 1
fi

FIXTURE="${ROOT}/lycaon/test/fixtures/e2e/hostile-project"
DEST="${STATE_DIR}/hostile-project"
mkdir -p "${DEST}"
cp -R "${FIXTURE}/." "${DEST}/"
# Recreate symlink after copy (cp -R may materialize it).
ln -sfn nested/deep/leaf.txt "${DEST}/symlink-leaf.txt"
HOSTILE_DIR="$(cd "${DEST}" && pwd -P)"

resp="$(curl -sf -X POST "${LYCAON_E2E_API_URL}/v1/projects" \
  -H "Authorization: Bearer ${LYCAON_E2E_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"Hostile\",\"roots\":[{\"path\":\"${HOSTILE_DIR}\"}]}")"

if command -v jq >/dev/null 2>&1; then
  id="$(printf '%s' "${resp}" | jq -r '.id // empty')"
else
  id="$(printf '%s' "${resp}" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi

echo "hostile-project: id=${id} path=${HOSTILE_DIR}"
echo "${resp}"

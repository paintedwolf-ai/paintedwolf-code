#!/usr/bin/env bash
# Source rules that need no build. Every rule runs so one pass reports them all.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
status=0

unformatted="$(git ls-files 'lycaon/*.go' 'scripts/*.go' | grep -Ev '/(testdata|vendor)/' | xargs gofmt -l)"
if [[ -n "${unformatted}" ]]; then
  printf 'not gofmt formatted:\n%s\n' "${unformatted}" >&2
  status=1
fi
python3 scripts/rewrite-fail-messages.py --check || status=1
(cd scripts/commentlint && go run . --root ../.. --config .commentlint.json) || status=1
bash scripts/doc-link-check.sh || status=1
exit "${status}"

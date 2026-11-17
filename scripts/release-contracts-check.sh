#!/usr/bin/env bash
# Verify catalog contracts and published migration identities against the
# stable release tags of this checkout.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

published="$(git -C "${ROOT}" tag --list 'v*' | python3 -c '
import json, re, sys
stable = re.compile(r"v([1-9][0-9]*\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))")
versions = [match[1] for tag in sys.stdin.read().split() if (match := stable.fullmatch(tag))]
if not versions:
    sys.exit("no published stable release tags; fetch tags before checking release contracts")
print(json.dumps(versions))
')"

cd "${ROOT}/lycaon"
LYCAON_ENGINE_ROOT="${LYCAON_ENGINE_ROOT:-${ROOT}/lycaon-den/src-tauri/engine-root}" \
  exec go run ./cmd/release-contracts --check-all --published-versions "${published}"

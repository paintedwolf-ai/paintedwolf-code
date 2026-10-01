#!/usr/bin/env bash
# Enforce aggregate coverage and report package coverage from one profile.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "check:coverage" -- bash "$0" "$@"
fi
cd "${ROOT}/lycaon"

MIN="$(python3 "$ROOT/scripts/coverage_policy.py" "${COVERAGE_MIN-62}")"

profile="$(mktemp -t lycaon-coverage.XXXXXX)"
trap 'rm -f "${profile}"' EXIT

echo "Running go test -short -coverprofile on ./internal/..."
bash "$ROOT/scripts/go-test-digest.sh" --name check:coverage -- -coverprofile="${profile}" ./internal/...

COVERAGE_PROFILE="$profile" bash "$ROOT/scripts/coverage-packages.sh"

total="$(go tool cover -func="${profile}" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')"
if [[ -z "${total}" ]]; then
  echo "coverage-check: could not parse total coverage" >&2
  exit 1
fi

total="$(python3 "$ROOT/scripts/coverage_policy.py" "$total")"

echo "internal/* statement coverage: ${total}% (minimum ${MIN}%)"
if ! python3 "$ROOT/scripts/coverage_policy.py" "$total" "$MIN"; then
  echo "coverage-check: FAILED — coverage ${total}% < ${MIN}%" >&2
  exit 1
fi

echo "coverage-check: OK"

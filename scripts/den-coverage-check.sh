#!/usr/bin/env bash
# Enforce lycaon-den Vitest coverage floors.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if ! python3 "$ROOT/scripts/test-execution.py" holding; then
  exec python3 "$ROOT/scripts/test-execution.py" run --name "den:coverage-check" -- bash "$0" "$@"
fi
cd "$ROOT/lycaon-den"

# Scoped to the packages listed in lycaon-den/vitest.config.ts coverage.include.
# Floors come from scripts/coverage-policy.json; DEN_COVERAGE_MIN_* overrides them.
policy() { python3 "$ROOT/scripts/coverage_policy.py" get "den.total.$1"; }
MIN_LINES="$(python3 "$ROOT/scripts/coverage_policy.py" "${DEN_COVERAGE_MIN_LINES-$(policy lines)}")"
MIN_FUNCS="$(python3 "$ROOT/scripts/coverage_policy.py" "${DEN_COVERAGE_MIN_FUNCTIONS-$(policy functions)}")"
MIN_BRANCH="$(python3 "$ROOT/scripts/coverage_policy.py" "${DEN_COVERAGE_MIN_BRANCHES-$(policy branches)}")"
MIN_STMT="$(python3 "$ROOT/scripts/coverage_policy.py" "${DEN_COVERAGE_MIN_STATEMENTS-$(policy statements)}")"

REPORT_DIR="$(mktemp -d -t den-coverage.XXXXXX)"
trap 'rm -rf "$REPORT_DIR"' EXIT
bash "$ROOT/scripts/vitest-digest.sh" --name den:coverage-check -- --coverage --coverage.reportsDirectory="$REPORT_DIR"

SUMMARY="$REPORT_DIR/coverage-summary.json"
if [[ ! -f "$SUMMARY" ]]; then
  echo "den-coverage-check: missing $SUMMARY" >&2
  exit 1
fi

# Environment assignments precede node.
# Reject non-numeric floors before comparison.
MIN_LINES="$MIN_LINES" MIN_FUNCS="$MIN_FUNCS" MIN_BRANCH="$MIN_BRANCH" MIN_STMT="$MIN_STMT" \
node -e "
const fs = require('fs');
const s = JSON.parse(fs.readFileSync(process.argv[1], 'utf8')).total;
const pct = (x) => {
  const value = x?.pct;
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 100) {
    throw new Error('den-coverage-check: missing or invalid coverage percentage');
  }
  return value;
};
const floor = (name) => {
  const v = Number(process.env[name]);
  if (!Number.isFinite(v)) {
    console.error(\`den-coverage-check: floor \${name} is not a number\`);
    process.exit(2);
  }
  return v;
};
const floors = {
  lines: floor('MIN_LINES'),
  functions: floor('MIN_FUNCS'),
  branches: floor('MIN_BRANCH'),
  statements: floor('MIN_STMT'),
};
const actual = {
  lines: pct(s.lines),
  functions: pct(s.functions),
  branches: pct(s.branches),
  statements: pct(s.statements),
};
let ok = true;
for (const [k, floor] of Object.entries(floors)) {
  if (actual[k] < floor) {
    console.error(\`den coverage \${k}: \${actual[k]}% < \${floor}%\`);
    ok = false;
  }
}
if (!ok) process.exit(1);
console.log('den coverage OK', actual);
" "$SUMMARY"

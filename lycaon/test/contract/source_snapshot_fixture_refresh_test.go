package contract

import (
	"os/exec"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Fixture refreshes run in a discarded snapshot, so their output must be
// copied back to the checkout without clobbering concurrent edits.
func TestSnapshotFixtureRefreshWritesBack(t *testing.T) {
	script := snapshotCacheFixture + `
# Drop inherited snapshot and refresh variables so they can't target the real checkout.
unset PW_SOURCE_ROOT_ORIGINAL PW_SOURCE_SNAPSHOT_COMMIT
for variable in $(compgen -e | grep '^UPDATE_' || true); do unset "$variable"; done
export TMPDIR="$2/tmp"
mkdir -p "$TMPDIR"

golden=lycaon/internal/report/testdata/golden
lock=lycaon/internal/db/schema.sql.lock.json
mkdir -p "$fixture/$golden" "$(dirname "$fixture/$lock")"
for name in a b c e; do printf "old-$name" > "$fixture/$golden/$name.pdf"; done
printf old-lock > "$fixture/$lock"
git -C "$fixture" add lycaon
git -C "$fixture" -c user.name=Test -c user.email=test@example.invalid commit -qm goldens
commit="$(git -C "$fixture" rev-parse HEAD)"

read_golden() { cat "$fixture/$golden/$1.pdf" 2>/dev/null || printf absent; }

# Rewrite a and b, add d, delete c; a concurrent edit changes b in the checkout.
refresh='
  set -euo pipefail
  printf new-a > '"$golden"'/a.pdf
  printf new-b > '"$golden"'/b.pdf
  printf new-d > '"$golden"'/d.pdf
  printf new-e > '"$golden"'/e.pdf
  rm '"$golden"'/c.pdf
  printf new-lock > '"$lock"'
  printf peer-b > "$1/'"$golden"'/b.pdf"
  rm -f "$1/'"$golden"'/e.pdf"
'

# No refresh variable: nothing is copied back.
bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- \
  bash -c "$refresh" refresh "$fixture" 2> "$2/plain.log"
[[ "$(read_golden a)" == old-a ]]
[[ "$(read_golden d)" == absent ]]
[[ "$(cat "$fixture/$lock")" == old-lock ]]
printf old-b > "$fixture/$golden/b.pdf"

# Changed and new files are copied; deletions and concurrently edited files are not.
UPDATE_REPORT_GOLDEN=1 bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- \
  bash -c "$refresh" refresh "$fixture" 2> "$2/refresh.log"
cat "$2/refresh.log"
[[ "$(read_golden a)" == new-a ]]
[[ "$(read_golden d)" == new-d ]]
[[ "$(read_golden c)" == old-c ]]
[[ "$(read_golden b)" == peer-b ]]
[[ "$(read_golden e)" == absent ]]
grep -q "golden/b.pdf changed in the checkout during the run; not overwriting" "$2/refresh.log"
# Each variable copies only its own paths.
[[ "$(cat "$fixture/$lock")" == old-lock ]]


UPDATE_SCHEMA_LOCK=1 bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- \
  bash -c "$refresh" refresh "$fixture" 2> "$2/lock.log"
[[ "$(cat "$fixture/$lock")" == new-lock ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script,
		"fixture-refresh-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot fixture refresh: %v\n%s", err, out)
	}
}

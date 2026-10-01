package contract

import (
	"os/exec"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSnapshotCaptureKeepsOnlyStableAttempt(t *testing.T) {
	for _, mode := range []string{"success", "failure", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			script := snapshotCacheFixture + `
for name in repo-snapshot-lock.sh snapshot-publish.sh; do
  cp "$source_root/scripts/$name" "$fixture/scripts/$name"
done
unset PW_REPO_SNAPSHOT_TOKEN PW_SOURCE_SNAPSHOT_COMMIT PW_SOURCE_ROOT_ORIGINAL
export PW_LOCK_ROOT="$fixture"
export PW_SNAPSHOT_READ_ATTEMPTS=2
export TMPDIR="$2/tmp"
export CAPTURE_TEST_ROOT="$2"
export CAPTURE_TEST_MODE="$3"
export CAPTURE_REAL_GIT="$(command -v git)"
mkdir -p "$TMPDIR" "$2/bin" "$fixture/repo-snapshot.lockdir"
printf '.task/\n' > "$fixture/.gitignore"
printf '0\n' > "$fixture/repo-snapshot.lockdir/epoch"
cat > "$2/bin/git" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  if [[ "$arg" == commit-tree ]]; then
    printf 'attempt\n' >> "$CAPTURE_TEST_ROOT/attempts"
    if [[ -f "$CAPTURE_TEST_ROOT/changed" && "$CAPTURE_TEST_MODE" == failure ]]; then
      echo 'capture test: stable attempt failed' >&2
      exit 23
    fi
    commit="$("$CAPTURE_REAL_GIT" "$@")"
    printf '%s\n' "$commit"
    printf '%s\n' "$commit" > "$CAPTURE_TEST_ROOT/expected-commit"
    if [[ ! -f "$CAPTURE_TEST_ROOT/changed" || "$CAPTURE_TEST_MODE" == exhausted ]]; then
      echo 'capture test: publishing changed source' >&2
      touch "$CAPTURE_TEST_ROOT/changed"
      epoch_file="$PW_LOCK_ROOT/repo-snapshot.lockdir/epoch"
      epoch="$(cat "$epoch_file")"
      printf '%s\n' "$((epoch + 2))" > "$epoch_file"
      printf '%s\n' "$((epoch + 2))" > "$PW_LOCK_ROOT/generated.txt"
    fi
    exit 0
  fi
done
exec "$CAPTURE_REAL_GIT" "$@"
SH
chmod +x "$2/bin/git"
export PATH="$2/bin:$PATH"
rc=0
bash "$fixture/scripts/test-source-snapshot.sh" run -- bash -c '
  [[ "$(cat generated.txt)" == 2 ]]
  git rev-parse HEAD
' > "$2/output" 2> "$2/diagnostics" || rc=$?
cat "$2/diagnostics"
[[ "$(wc -l < "$2/attempts" | tr -d ' ')" == 2 ]]
grep -q 'capture test: publishing changed source' "$2/diagnostics"
grep -q 'generated files changed while this command ran' "$2/diagnostics"
case "$CAPTURE_TEST_MODE" in
  success)
    [[ "$rc" == 0 ]]
    [[ "$(cat "$2/output")" == "$(cat "$2/expected-commit")" ]]
    [[ "$(wc -l < "$2/output" | tr -d ' ')" == 1 ]]
    "$CAPTURE_REAL_GIT" -C "$fixture" cat-file -e "$(cat "$2/output")^{commit}"
    ;;
  failure)
    [[ "$rc" == 23 && ! -s "$2/output" ]]
    grep -q 'capture test: stable attempt failed' "$2/diagnostics"
    ;;
  exhausted)
    [[ "$rc" == 1 && ! -s "$2/output" ]]
    grep -q 'generated files kept changing across 2 attempts' "$2/diagnostics"
    ;;
esac
[[ ! -e "$PW_LOCK_ROOT/source-capture.lockdir" ]]
shopt -s nullglob
remaining=("$TMPDIR"/paintedwolf-capture.* "$TMPDIR"/paintedwolf-index.*)
[[ "${#remaining[@]}" == 0 ]]
`
			cmd := exec.CommandContext(t.Context(), "bash", "-c", script,
				"capture-test", contractcheck.RepoRoot(t), t.TempDir(), mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("snapshot capture %s: %v\n%s", mode, err, out)
			}
		})
	}
}

package contract

import (
	"os/exec"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSnapshotRetainsCompilerCacheAndIsolatesExecutables(t *testing.T) {
	// A hosted runner verifies only its job's source and keeps the checkout's cache; elsewhere each
	// snapshot slot keeps its own, so one source's targets never pass for another's.
	script := snapshotCacheFixture + `for hosted in true false; do
unset PW_COMPILER_CACHE_DIR
export GITHUB_ACTIONS="$hosted"
for status in 0 7; do
  rc=0
  bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- bash -c '
    set -euo pipefail
    base="$(cd "$1" && pwd -P)/artifacts/compiler-cache"
    if [[ "$3" == true ]]; then [[ "$PW_COMPILER_CACHE_DIR" == "$base" ]]; else [[ "$PW_COMPILER_CACHE_DIR" == "$base"/slots/* ]]; fi
    [[ "$(python3 scripts/artifact_paths.py compiler-cache "$PWD")" == "$PW_COMPILER_CACHE_DIR" ]]
    [[ "$PW_COMPILER_CACHE_DIR" != "$PW_BUILD_DIR"/* ]]
    mkdir -p "$PW_COMPILER_CACHE_DIR/decide" "$PW_BUILD_DIR"
    if [[ "$2" == 7 ]]; then test -f "$PW_COMPILER_CACHE_DIR/decide/retained"; fi
    touch "$PW_COMPILER_CACHE_DIR/decide/retained" "$PW_BUILD_DIR/bialy"
    printf "%s" "$PW_BUILD_DIR" > "$1/native-output"
    printf "%s" "$PW_COMPILER_CACHE_DIR" > "$1/cache-dir"
    exit "$2"
  ' compiler-test "$2" "$status" "$hosted" || rc=$?
  [[ "$rc" == "$status" ]]
  [[ -f "$(cat "$2/cache-dir")/decide/retained" ]]
  [[ ! -e "$(cat "$2/native-output")" ]]
done
done
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "compiler-cache-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native compiler cache lifecycle: %v\n%s", err, out)
	}
}

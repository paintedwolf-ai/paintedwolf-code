package contract

import (
	"os/exec"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSnapshotRetainsCompilerCacheAndIsolatesExecutables(t *testing.T) {
	script := snapshotCacheFixture + `unset PW_COMPILER_CACHE_DIR
for status in 0 7; do
  rc=0
  bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- bash -c '
    set -euo pipefail
    [[ "$PW_COMPILER_CACHE_DIR" == "$1/artifacts/compiler-cache" ]]
    [[ "$(python3 scripts/artifact_paths.py compiler-cache "$PWD")" == "$PW_COMPILER_CACHE_DIR" ]]
    [[ "$PW_COMPILER_CACHE_DIR" != "$PW_BUILD_DIR"/* ]]
    mkdir -p "$PW_COMPILER_CACHE_DIR/decide" "$PW_BUILD_DIR"
    if [[ "$2" == 7 ]]; then test -f "$PW_COMPILER_CACHE_DIR/decide/retained"; fi
    touch "$PW_COMPILER_CACHE_DIR/decide/retained" "$PW_BUILD_DIR/bialy"
    printf "%s" "$PW_BUILD_DIR" > "$1/native-output"
    exit "$2"
  ' compiler-test "$2" "$status" || rc=$?
  [[ "$rc" == "$status" ]]
  [[ -f "$2/artifacts/compiler-cache/decide/retained" ]]
  [[ ! -e "$(cat "$2/native-output")" ]]
done
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "compiler-cache-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native compiler cache lifecycle: %v\n%s", err, out)
	}
}

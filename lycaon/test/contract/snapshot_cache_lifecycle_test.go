package contract

import (
	"os/exec"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const snapshotCacheFixture = `set -euo pipefail
source_root="$1"
fixture="$2/repo"
mkdir -p "$fixture/scripts"
for name in test-source-snapshot.sh checkout-cache-dir.sh test-run-isolation.sh test-run-lease.py digest-run-lock.sh artifact-paths.sh artifact_paths.py; do
  cp "$source_root/scripts/$name" "$fixture/scripts/$name"
done
printf 'module example.invalid/cachefixture\n\ngo 1.26.6\n' > "$fixture/go.mod"
printf 'package cachefixture\n\nfunc Value() int { return 1 }\n' > "$fixture/value.go"
git -C "$fixture" init -q
git -C "$fixture" add scripts go.mod value.go
git -C "$fixture" -c user.name=Test -c user.email=test@example.invalid commit -qm fixture
commit="$(git -C "$fixture" rev-parse HEAD)"
export PW_TEST_SNAPSHOT_ROOT="$2/snapshots"
export PW_ARTIFACT_ROOT="$2/artifacts" PW_TEST_ARTIFACT_ROOT="$2/artifacts"
export PW_BIN_DIR="$2/tools" PW_BUILD_DIR="$2/build" PW_LOCK_ROOT="$2/locks"
unset OPENGREP_STAGE_DIR
`

func TestSnapshotLintCacheLifecycle(t *testing.T) {
	script := snapshotCacheFixture + `export PW_TEST_CHECKOUT_CACHE="$2/parent-cache"
export GOCACHE="$2/compiled-cache"
mkdir -p "$GOCACHE"
touch "$GOCACHE/object"
mkdir -p "$PW_TEST_CHECKOUT_CACHE"
touch "$PW_TEST_CHECKOUT_CACHE/keep"
for status in 0 7; do
  rc=0
  bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- bash -c '
    set -euo pipefail
    cache="$(bash scripts/checkout-cache-dir.sh)"
    [[ "$cache" != "$1/parent-cache" ]]
    [[ "$cache" != "$PWD"/* ]]
    [[ "$(HOME="$PWD" bash scripts/checkout-cache-dir.sh)" == "$cache" ]]
    [[ "$GOCACHE" == "$1/compiled-cache" ]]
    [[ -f "$GOCACHE/object" ]]
    [[ ! -e previous-run ]]
    touch previous-run
    compiled="$(go list -export -f '{{.Export}}' .)"
    if [[ -f "$1/compiled-path" ]]; then
      [[ "$(cat "$1/compiled-path")" == "$compiled" ]]
      [[ "$(cat "$1/snapshot-path")" == "$PWD" ]]
    fi
    printf "%s" "$compiled" > "$1/compiled-path"
    printf "%s" "$PWD" > "$1/snapshot-path"
    mkdir -p "$cache/golangci-cache-test"
    touch "$cache/golangci-cache-test/diagnostic"
    printf "%s" "$cache" > "$1/cache-path"
    [[ "$PW_BIN_DIR" == "$1/tools" ]]
    [[ "$OPENGREP_STAGE_DIR" == "$1/build/opengrep-bundle" ]]
    [[ "$PW_BUILD_DIR" != "$1/build" && "$PW_BUILD_DIR" != "$PWD"/* ]]
    mkdir -p "$PW_BUILD_DIR"
    touch "$PW_BUILD_DIR/lycaon-dev"
    printf "%s" "$PW_BUILD_DIR" > "$1/build-path"
    exit "$2"
  ' cache-test "$2" "$status" || rc=$?
  [[ "$rc" == "$status" ]]
  [[ ! -e "$(cat "$2/cache-path")" ]]
  [[ ! -e "$(cat "$2/build-path")" && ! -e "$2/build/lycaon-dev" ]]
  [[ -f "$PW_TEST_CHECKOUT_CACHE/keep" ]]
  [[ -f "$GOCACHE/object" ]]
done
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "cache-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot cache lifecycle: %v\n%s", err, out)
	}
}

func TestSnapshotCacheCreationFailureReleasesCollectionLock(t *testing.T) {
	script := snapshotCacheFixture + `export PW_CACHE_FAILURE_ROOT="$2/cache-root"
cat >> "$fixture/scripts/test-run-isolation.sh" <<'SH'
test_run_create_isolation() {
  TEST_RUN_ROOT="$PW_CACHE_FAILURE_ROOT"
  mkdir -p "$TEST_RUN_ROOT"
  exec 9>"$TEST_RUN_ROOT/.collection.lock"
  python3 "$TEST_RUN_LEASE_HELPER" lock 9
  return 19
}
test_run_prune_abandoned() {
  python3 - "$TEST_RUN_ROOT/.collection.lock" > "$TEST_RUN_ROOT/probe" 2>&1 <<'PY'
import fcntl
import sys
with open(sys.argv[1], 'a') as collection:
    fcntl.flock(collection, fcntl.LOCK_EX | fcntl.LOCK_NB)
PY
}
SH
rc=0
bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- true || rc=$?
[[ "$rc" == 19 ]]
cat "$PW_CACHE_FAILURE_ROOT/probe"
[[ ! -s "$PW_CACHE_FAILURE_ROOT/probe" ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "creation-failure-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot cache creation failure: %v\n%s", err, out)
	}
}

func TestSnapshotCacheLeasePreservesDescendant(t *testing.T) {
	script := `set -euo pipefail
source "$1/scripts/test-run-isolation.sh"
TEST_RUN_ROOT="$2/runs"
test_run_create_isolation snapshot-cache
cache="$TEST_RUN_DIR"
mkfifo "$2/release"
bash -c 'touch "$1/ready"; read -r release < "$1/release"' child "$2" &
child=$!
trap 'kill "$child" 2>/dev/null || true' EXIT
for ((attempt=0; attempt<250; attempt++)); do
  [[ ! -f "$2/ready" ]] || break
  sleep 0.02
done
[[ -f "$2/ready" ]]
exec 8>&-
test_run_prune_abandoned
[[ -d "$cache" ]]
printf 'release\n' > "$2/release"
wait "$child"
trap - EXIT
test_run_prune_abandoned
[[ ! -e "$cache" ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "lease-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot descendant lease: %v\n%s", err, out)
	}
}

func TestSnapshotSlotsKeepActiveRunsSeparate(t *testing.T) {
	script := snapshotCacheFixture + `mkfifo "$2/release"
bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- bash -c '
  printf "%s" "$PWD" > "$1/first-path"
  read -r release < "$1/release"
' slot-test "$2" &
first=$!
trap 'kill "$first" 2>/dev/null || true' EXIT
for ((attempt=0; attempt<250; attempt++)); do
  [[ ! -f "$2/first-path" ]] || break
  sleep 0.02
done
[[ -f "$2/first-path" ]]
bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- pwd > "$2/second-path"
[[ "$(cat "$2/first-path")" != "$(cat "$2/second-path")" ]]
[[ -f "$(cat "$2/first-path")/value.go" ]]
printf 'release\n' > "$2/release"
wait "$first"
trap - EXIT
bash "$fixture/scripts/test-source-snapshot.sh" run-commit "$commit" -- pwd > "$2/reused-path"
[[ "$(cat "$2/first-path")" == "$(cat "$2/reused-path")" ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "slot-test", contractcheck.RepoRoot(t), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot slot isolation: %v\n%s", err, out)
	}
}

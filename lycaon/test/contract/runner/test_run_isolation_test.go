package contract

import (
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestTestRunIsolationPreservesLiveOwnerAcrossTimezones(t *testing.T) {
	script := `set -euo pipefail
source "$1"
TEST_RUN_ROOT="$2"
export TZ=America/Los_Angeles
test_run_create_isolation go-test
run_dir="$TEST_RUN_DIR"
export TZ=UTC
test_run_prune_abandoned
[[ -d "$run_dir" ]]
test_run_remove_isolation "$run_dir"
[[ ! -d "$run_dir" ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "isolation-test",
		filepath.Join(contractcheck.RepoRoot(t), "scripts", "test-run-isolation.sh"), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live-owner reclamation: %v\n%s", err, out)
	}
}

func TestTestRunIsolationReclaimsExitedOwner(t *testing.T) {
	script := `set -euo pipefail
source "$1"
TEST_RUN_ROOT="$2"
bash -c 'source "$1"; TEST_RUN_ROOT="$2"; test_run_create_isolation go-test' child "$1" "$2"
test_run_prune_abandoned
shopt -s nullglob
remaining=("$TEST_RUN_ROOT"/*)
[[ ${#remaining[@]} -eq 0 ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "isolation-test",
		filepath.Join(contractcheck.RepoRoot(t), "scripts", "test-run-isolation.sh"), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exited-owner reclamation: %v\n%s", err, out)
	}
}

func TestTestRunIsolationPreservesInheritedLease(t *testing.T) {
	script := `set -euo pipefail
source "$1"
TEST_RUN_ROOT="$2/runs"
test_run_create_isolation go-test
run_dir="$TEST_RUN_DIR"
mkfifo "$2/release"
bash -c 'read -r _ < "$1"' child "$2/release" &
child=$!
trap 'kill "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true' EXIT
test_run_remove_isolation "$run_dir"
[[ -d "$run_dir" ]]
printf 'release\n' > "$2/release"
wait "$child"
trap - EXIT
test_run_prune_abandoned
[[ ! -d "$run_dir" ]]
`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "isolation-test",
		filepath.Join(contractcheck.RepoRoot(t), "scripts", "test-run-isolation.sh"), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inherited-owner reclamation: %v\n%s", err, out)
	}
}

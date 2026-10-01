package contract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestHarnessLeasedTeardownDeletesReadOnlySourceSnapshots(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	stateDir := filepath.Join(t.TempDir(), "state with spaces")
	snapshotDir := filepath.Join(stateDir, "source-snapshots", "session", "worker")
	contractcheck.FailErr(t, "create source snapshot", os.MkdirAll(snapshotDir, 0o700))
	contractcheck.FailErr(t, "write source snapshot", os.WriteFile(
		filepath.Join(snapshotDir, "source.go"),
		[]byte("package source\n"),
		0o400,
	))
	contractcheck.FailErr(t, "write harness lease", os.WriteFile(
		filepath.Join(stateDir, ".harness-lease"),
		[]byte("99999999\ntest-token\n"),
		0o600,
	))

	for _, dir := range []string{snapshotDir, filepath.Dir(snapshotDir), filepath.Dir(filepath.Dir(snapshotDir)), stateDir} {
		contractcheck.FailErr(t, "make source snapshot read-only", os.Chmod(dir, 0o500))
	}
	t.Cleanup(func() {
		_ = os.Chmod(stateDir, 0o700)
		_ = os.Chmod(filepath.Join(stateDir, "source-snapshots"), 0o700)
		_ = os.Chmod(filepath.Join(stateDir, "source-snapshots", "session"), 0o700)
		_ = os.Chmod(snapshotDir, 0o700)
		_ = os.RemoveAll(stateDir)
	})

	lib := filepath.Join(root, "scripts", "harness", "lib.sh")
	cmd := exec.Command("bash", "-c", `source "$1"; harness_remove_leased_state "$2" test-token`, "harness-cleanup", lib, stateDir)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remove read-only harness state: %v\n%s", err, output)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("harness state still exists after teardown: %v", err)
	}
}

func TestHarnessLeaseReaperOnlyCollectsDeadHolders(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	base := t.TempDir()
	lib := filepath.Join(root, "scripts", "harness", "lib.sh")

	live := filepath.Join(base, "lycaon-harness.live")
	contractcheck.FailErr(t, "create live state", os.MkdirAll(live, 0o700))
	contractcheck.FailErr(t, "bind live lease", os.WriteFile(
		filepath.Join(live, ".harness-lease"),
		[]byte(fmt.Sprintf("%d\nlive-token\n", os.Getpid())),
		0o600,
	))

	stale := filepath.Join(base, "lycaon-harness.stale")
	contractcheck.FailErr(t, "create stale state", os.MkdirAll(stale, 0o700))
	contractcheck.FailErr(t, "write stale lease", os.WriteFile(
		filepath.Join(stale, ".harness-lease"),
		[]byte("99999999\nstale-token\n"),
		0o600,
	))

	cmd := exec.Command("bash", "-c", `source "$1"; harness_reap_stale_states "$2"`,
		"harness-reaper", lib, base)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reap harness states: %v\n%s", err, output)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live lease was collected: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale lease remains: %v", err)
	}
}

func TestHarnessClaimReplacesStaleState(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	stateDir := filepath.Join(t.TempDir(), "lycaon-harness.stale-claim")
	contractcheck.FailErr(t, "create stale state", os.MkdirAll(stateDir, 0o700))
	contractcheck.FailErr(t, "write stale lease", os.WriteFile(
		filepath.Join(stateDir, ".harness-lease"),
		[]byte("99999999\nstale-token\n"),
		0o600,
	))
	staleFile := filepath.Join(stateDir, "stale-data")
	contractcheck.FailErr(t, "write stale data", os.WriteFile(staleFile, []byte("stale"), 0o600))

	lib := filepath.Join(root, "scripts", "harness", "lib.sh")
	cmd := exec.Command("bash", "-c", `source "$1"; harness_claim_state "$2"`,
		"harness-claim", lib, stateDir)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claim stale harness state: %v\n%s", err, output)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("stale data remains after claim: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".harness-lease")); err != nil {
		t.Fatalf("claimed lease missing: %v", err)
	}
}

func TestHarnessLeaseTokenPreventsForeignTeardown(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	stateDir := filepath.Join(t.TempDir(), "lycaon-harness.foreign")
	contractcheck.FailErr(t, "create state", os.MkdirAll(stateDir, 0o700))
	contractcheck.FailErr(t, "write lease", os.WriteFile(
		filepath.Join(stateDir, ".harness-lease"),
		[]byte("99999999\nactual-token\n"),
		0o600,
	))

	lib := filepath.Join(root, "scripts", "harness", "lib.sh")
	cmd := exec.Command("bash", "-c",
		`source "$1"; harness_remove_leased_state "$2" wrong-token`,
		"harness-token", lib, stateDir)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("foreign lease removal unexpectedly succeeded: %s", output)
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("foreign lease state was removed: %v", err)
	}
}

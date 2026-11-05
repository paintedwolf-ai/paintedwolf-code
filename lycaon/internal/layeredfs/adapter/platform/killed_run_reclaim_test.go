//go:build darwin || linux

package platform_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/testutil"
)

const killedRunHolderEnv = "LYCAON_KILLED_RUN_HOLDER"

// TestRunnerReclaimsOnlyTheMountsOfAKilledRun drives the runner's own lease helper. While a run holds
// its lease its mount stays. Once the killed process has released the lease the helper removes that
// mount and the run directory, and leaves a live mount outside the run alone.
func TestRunnerReclaimsOnlyTheMountsOfAKilledRun(t *testing.T) {
	if base := os.Getenv(killedRunHolderEnv); base != "" {
		holdMountUntilKilled(t, base)
		return
	}
	mounted := selectMountedAdapter(t)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "runs")
	run := filepath.Join(root, "go-test.lease.killed")
	testutil.FailErr(t, "create run directory", os.MkdirAll(filepath.Join(run, "tmp"), 0o700))
	lease, err := os.Create(filepath.Join(run, ".lease"))
	testutil.FailErr(t, "create lease", err)
	testutil.FailErr(t, "hold lease", syscall.Flock(int(lease.Fd()), syscall.LOCK_EX))

	outside := prepareMountedRoot(t, mounted, filepath.Join(tmp, "outside"), "view-outside")
	t.Cleanup(func() { _, _ = mounted.Release(context.Background(), "view-outside") })
	outsideConnections := recordedConnections(t, outside)

	exe, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	holder := exec.Command(exe, "-test.run=^TestRunnerReclaimsOnlyTheMountsOfAKilledRun$")
	holder.Env = append(os.Environ(), killedRunHolderEnv+"="+filepath.Join(run, "tmp"))
	holder.Stderr = os.Stderr
	// The holder inherits the lease description the way a test binary inherits its run's.
	holder.ExtraFiles = []*os.File{lease}
	stdout, err := holder.StdoutPipe()
	testutil.FailErr(t, "holder stdout", err)
	stdin, err := holder.StdinPipe()
	testutil.FailErr(t, "holder stdin", err)
	testutil.FailErr(t, "start holder", holder.Start())
	killed := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !killed {
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	})
	testutil.FailErr(t, "close the parent lease", lease.Close())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "holder runtime directory", err)
	runConnections := recordedConnectionsIn(t, strings.TrimSpace(line))

	settle := platform.ConnectionRecoveries()[mounted.ID()]
	requireDisposition(t, settle, runConnections, adapter.ConnectionServerAlive)
	pruneRuns(t, root)
	requireDisposition(t, settle, runConnections, adapter.ConnectionServerAlive)

	testutil.FailErr(t, "kill holder", holder.Process.Kill())
	_ = holder.Wait()
	killed = true
	pruneRuns(t, root)
	requireDisposition(t, settle, runConnections, adapter.ConnectionAbsent)
	if _, err := os.Lstat(run); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("run directory after reclaim: %v", err)
	}
	requireDisposition(t, settle, outsideConnections, adapter.ConnectionServerAlive)
	if _, err := os.ReadDir(outside.PrimaryWorkPath); err != nil {
		t.Fatalf("mount outside the run was affected: %v", err)
	}
}

func holdMountUntilKilled(t *testing.T, base string) {
	view := prepareMountedRoot(t, selectMountedAdapter(t), base, "view-killed")
	fmt.Println(view.RuntimeViewDir)
	_, _ = os.Stdin.Read(make([]byte, 1))
}

func selectMountedAdapter(t *testing.T) adapter.Adapter {
	t.Helper()
	mounted, _, err := platform.NewSelector(platform.Config{Confinement: availableConfinement{}}).
		Select(t.Context(), adapter.ProbeConfig{RuntimeDir: t.TempDir()})
	testutil.FailErr(t, "select adapter", err)
	if mounted.Facts().PreparationKind != adapter.PreparationMounted {
		t.Fatalf("selected %s, which mounts nothing", mounted.ID())
	}
	return mounted
}

func prepareMountedRoot(t *testing.T, mounted adapter.Adapter, base, viewID string) *adapter.PreparedView {
	t.Helper()
	trunk := filepath.Join(base, "trunk")
	testutil.FailErr(t, "create trunk", os.MkdirAll(trunk, 0o755))
	view, err := mounted.Prepare(context.Background(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: filepath.Join(base, "backing"), RuntimeDir: filepath.Join(base, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "prepare mounted view", err)
	return view
}

func recordedConnections(t *testing.T, view *adapter.PreparedView) []adapter.RecordedConnection {
	t.Helper()
	return recordedConnectionsIn(t, view.RuntimeViewDir)
}

func recordedConnectionsIn(t *testing.T, runtimeViewDir string) []adapter.RecordedConnection {
	t.Helper()
	connections, err := adapter.LoadRecordedConnections(runtimeViewDir)
	testutil.FailErr(t, "load recorded connections", err)
	if len(connections) != 1 {
		t.Fatalf("recorded connections at %s: %+v", runtimeViewDir, connections)
	}
	return connections
}

func requireDisposition(t *testing.T, settle adapter.ConnectionRecovery, connections []adapter.RecordedConnection, want adapter.ConnectionDisposition) {
	t.Helper()
	for _, connection := range connections {
		settlement, err := settle.SettleRecordedConnection(t.Context(), connection)
		if err != nil || settlement.Disposition != want {
			t.Fatalf("connection at %s: want %s, got %+v err=%v", connection.MountPath, want, settlement, err)
		}
	}
}

func pruneRuns(t *testing.T, root string) {
	t.Helper()
	helper, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "scripts", "test-run-lease.py"))
	testutil.FailErr(t, "resolve lease helper", err)
	if out, err := exec.Command("python3", helper, "prune", root).CombinedOutput(); err != nil {
		t.Fatalf("prune: %v (%s)", err, out)
	}
}

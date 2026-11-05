//go:build darwin || linux

package delegations

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

const realEngineHelperEnv = "LYCAON_DELEGATIONS_REAL_ENGINE"

// realEngineAdapter is what the dying engine needs from the host's real mounted adapter.
type realEngineAdapter interface {
	adapter.Adapter
	NewBarrierProvider(viewID string) checkpoint.BarrierCallbacks
}

// realEngineHost is the part of the killed-engine test that differs by host.
type realEngineHost struct {
	adapter           realEngineAdapter
	quiesce           func(context.Context, string) error
	flushClients      func(context.Context, string) error
	requireAvailable  func(*testing.T)
	requireServerGone func(t *testing.T, mountPath string)
	detach            func(mountPath string)
}

// TestRealMountKilledEngineRecoversThroughStartup [process-crash evidence, not physical
// power-loss proof] kills a real serving process with a holder inside its mount, then a new
// engine settles it through the production startup entrypoint and reconstructs the checkpoint.
func TestRealMountKilledEngineRecoversThroughStartup(t *testing.T) {
	if os.Getenv(realEngineHelperEnv) != "" {
		runRealEngine(t)
		return
	}
	mountedHost := newRealEngineHost()
	mountedHost.requireAvailable(t)
	f := newEngineFixture(t)
	f.engine.Workspace.WithSelector(nil)
	task := f.claimDefaultWorker()
	projectKey := filepath.Base(filepath.Dir(task.WorkspaceRoot))
	branchRoot := enginepaths.WorkerBranchesRootUnder(f.stateRoot)
	runtimeDir := filepath.Join(branchRoot, ".adapter", projectKey)
	backing := filepath.Join(runtimeDir, task.ID+".backing")
	metaDir := enginepaths.MetaDirForBranchRoot(task.WorkspaceRoot)
	meta, err := workspace.LoadJobMeta(metaDir)
	testutil.FailErr(t, "load meta", err)
	meta.AdapterID, meta.PreparationKind, meta.ViewID = mountedHost.adapter.ID(), adapter.PreparationMounted, task.ID
	meta.JournalPath = filepath.Join(backing, "journal")
	testutil.FailErr(t, "write meta", workspace.WriteJobMeta(metaDir, meta))
	testutil.FailErr(t, "empty the mount point", clearDir(task.WorkspaceRoot))

	exe, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	engine := exec.Command(exe, "-test.run=^TestRealMountKilledEngineRecoversThroughStartup$")
	engine.Env = append(os.Environ(), realEngineHelperEnv+"=1",
		"HELPER_TASK="+task.ID, "HELPER_ROOT="+task.WorkspaceRoot, "HELPER_RUNTIME="+runtimeDir, "HELPER_BACKING="+backing,
		"HELPER_PROJECT="+f.project, "HELPER_STORE="+filepath.Join(enginepaths.WorkerRecoveryRootUnder(f.stateRoot), "checkpoints"),
		"HELPER_STATE="+filepath.Join(enginepaths.WorkerRecoveryRootUnder(f.stateRoot), "state"), "HELPER_KEY="+projectKey)
	engine.Stderr = os.Stderr
	stdout, err := engine.StdoutPipe()
	testutil.FailErr(t, "helper stdout", err)
	stdin, err := engine.StdinPipe()
	testutil.FailErr(t, "helper stdin", err)
	defer func() { _ = stdin.Close() }()
	testutil.FailErr(t, "start helper engine", engine.Start())
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = engine.Process.Kill()
			_ = engine.Wait()
		}
		mountedHost.detach(task.WorkspaceRoot)
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper engine not ready: %q err=%v", line, err)
	}

	holder := exec.Command("/bin/sh", "-c", `cd "$1" && echo inside; read _; exit 0`, "sh", task.WorkspaceRoot)
	holderIn, err := holder.StdinPipe()
	testutil.FailErr(t, "holder stdin", err)
	holderOut, err := holder.StdoutPipe()
	testutil.FailErr(t, "holder stdout", err)
	testutil.FailErr(t, "start holder", holder.Start())
	if line, err := bufio.NewReader(holderOut).ReadString('\n'); err != nil || line != "inside\n" {
		t.Fatalf("holder never entered the mount: %q err=%v", line, err)
	}
	holderDone := make(chan error, 1)
	go func() { holderDone <- holder.Wait() }()
	t.Cleanup(func() { _ = holderIn.Close(); _ = holder.Process.Kill() })

	testutil.FailErr(t, "kill engine", engine.Process.Kill())
	_ = engine.Wait()
	killed = true
	mountedHost.requireServerGone(t, task.WorkspaceRoot)
	if err := workspace.RefuseMountedBranch(task.WorkspaceRoot); !errors.Is(err, workspace.ErrBranchMounted) {
		t.Fatalf("a dead mount must refuse deletion: %v", err)
	}

	coordinator, err := newWorkerRecovery(f.stateRoot, nil)
	testutil.FailErr(t, "production coordinator", err)
	next := f.boot()
	next.Recovery = coordinator
	next.Workspace = workspace.NewManager(next.BranchRoot, next.SeedRoot).WithRecovery(coordinator)
	next.Queue.SetWorkerWorkspaceManager(next.Workspace)
	testutil.FailErr(t, "startup recovery", next.RecoverWorkerViews(t.Context()))

	if err := workspace.RefuseMountedBranch(task.WorkspaceRoot); err != nil {
		t.Fatalf("mount remains after recovery: %v", err)
	}
	if err := holder.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("holder inside the mount was affected: %v", err)
	}
	if resumed, ok := next.Queue.Get(task.ID); !ok || resumed.Status.IsTerminal() {
		t.Fatalf("worker did not resume from its checkpoint: %+v", resumed)
	}
	if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "checkpoint-A\n" {
		t.Fatalf("rebuilt branch shows %q", got)
	}
	records, err := next.Recovery.ListInterruptedRecords(task.ID)
	testutil.FailErr(t, "interrupted", err)
	if len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, "unsealed-after-A\n") {
		t.Fatalf("interrupted work: %+v", records)
	}
	if idents, err := next.Recovery.ListMountIdentities(task.ID); err != nil {
		t.Fatalf("identities: %v", err)
	} else if len(idents) != 1 || idents[0].ViewID == task.ID {
		t.Fatalf("killed view identity must be gone, rebuilt view recorded: %+v", idents)
	}
	_ = holderIn.Close()
	if err := <-holderDone; err != nil {
		t.Fatalf("holder exit: %v", err)
	}
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// runRealEngine is the engine that dies: it serves a real mount, publishes through it and
// leaves later work unsealed.
func runRealEngine(t *testing.T) {
	task, root := os.Getenv("HELPER_TASK"), os.Getenv("HELPER_ROOT")
	store, err := checkpoint.NewStore(os.Getenv("HELPER_STORE"))
	testutil.FailErr(t, "helper store", err)
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{RootDir: os.Getenv("HELPER_STATE"), CheckpointStore: store})
	testutil.FailErr(t, "helper coordinator", err)
	mountedHost := newRealEngineHost()
	adp := mountedHost.adapter
	view, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: task, RuntimeDir: os.Getenv("HELPER_RUNTIME"), BackingDir: os.Getenv("HELPER_BACKING"),
		TargetRootDirs: map[string]string{fixtureRootID: root},
		QuiesceHook:    mountedHost.quiesce, FlushClientHook: mountedHost.flushClients,
		Grants: []adapter.RootGrant{{RootID: fixtureRootID, Label: fixtureRootID, SourcePath: os.Getenv("HELPER_PROJECT"), IsPrimary: true}},
	})
	testutil.FailErr(t, "helper prepare", err)
	testutil.FailErr(t, "helper journal", coordinator.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		WorkerID: task, ViewID: task, TaskID: task, ProjectID: os.Getenv("HELPER_KEY"), AdapterID: adp.ID(), BackingDir: view.BackingDir,
	}))
	testutil.FailErr(t, "helper write", os.WriteFile(filepath.Join(root, "app.txt"), []byte("checkpoint-A\n"), 0o644))
	publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: view.Core, Store: store, WorkerID: task, TaskID: task, ProjectID: os.Getenv("HELPER_KEY"), SourceGeneration: "1",
		SelectedRoots: []layeredfs.RootSpec{{ID: fixtureRootID, Label: fixtureRootID, TrunkPath: os.Getenv("HELPER_PROJECT"), IsPrimary: true}},
		Callbacks:     adp.NewBarrierProvider(task),
	})
	testutil.FailErr(t, "helper publisher", err)
	_, err = publisher.Publish(context.Background(), checkpoint.PublishRequest{CheckpointID: "cp-A"})
	testutil.FailErr(t, "helper publish", err)
	testutil.FailErr(t, "helper journal publish", coordinator.RecordTransition(task, task, recovery.PhasePublication, recovery.StateCommitted, "cp-A", "publish", nil, false))
	testutil.FailErr(t, "helper unsealed", os.WriteFile(filepath.Join(root, "app.txt"), []byte("unsealed-after-A\n"), 0o644))
	file, err := os.Open(filepath.Join(root, "app.txt"))
	testutil.FailErr(t, "helper reopen", err)
	testutil.FailErr(t, "helper sync", file.Sync())
	_ = file.Close()
	fmt.Println("ready")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

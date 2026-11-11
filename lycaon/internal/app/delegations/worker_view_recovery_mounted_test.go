//go:build darwin || linux

package delegations

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/osprocess"
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

// helperWorker is one mounted worker the dying engine serves.
type helperWorker struct {
	Task, Root, Runtime, Backing, Key string
}

// TestRealMountKilledEngineRecoversThroughStartup [process-crash evidence, not physical
// power-loss proof] kills a real serving process with several mounted workers and a holder inside
// one mount. A new engine settles every recorded mount through the production startup entrypoint.
// The worker whose engine exited in this boot resumes its working storage, names and bytes; the one
// recorded under an earlier boot rebuilds from its checkpoint and keeps the rest aside. A mount the
// engine never recorded, beside them, is untouched.
func TestRealMountKilledEngineRecoversThroughStartup(t *testing.T) {
	if os.Getenv(realEngineHelperEnv) != "" {
		runRealEngine(t)
		return
	}
	mountedHost := newRealEngineHost()
	mountedHost.requireAvailable(t)
	f := newEngineFixture(t)
	f.engine.Workspace.WithSelector(nil)
	branchRoot := enginepaths.WorkerBranchesRootUnder(f.stateRoot)
	var workers []helperWorker
	for range 2 {
		task := f.claimDefaultWorker()
		projectKey := filepath.Base(filepath.Dir(task.WorkspaceRoot))
		runtimeDir := filepath.Join(branchRoot, ".adapter", projectKey)
		backing := filepath.Join(runtimeDir, task.ID+".backing")
		metaDir := enginepaths.MetaDirForBranchRoot(task.WorkspaceRoot)
		meta, err := workspace.LoadJobMeta(metaDir)
		testutil.FailErr(t, "load meta", err)
		meta.AdapterID, meta.PreparationKind, meta.ViewID = mountedHost.adapter.ID(), adapter.PreparationMounted, task.ID
		meta.JournalPath = filepath.Join(backing, "journal")
		testutil.FailErr(t, "write meta", workspace.WriteJobMeta(metaDir, meta))
		testutil.FailErr(t, "empty the mount point", clearDir(task.WorkspaceRoot))
		workers = append(workers, helperWorker{Task: task.ID, Root: task.WorkspaceRoot, Runtime: runtimeDir, Backing: backing, Key: projectKey})
	}
	// The foreign mount sits beside the workers' branches under a name like theirs, served by the
	// same adapter, with its records kept where the engine never looks.
	foreignRoot := workers[0].Root + "-foreign"
	testutil.FailErr(t, "foreign mount point", os.MkdirAll(foreignRoot, 0o755))
	foreignRuntime := t.TempDir()
	encoded, err := json.Marshal(workers)
	testutil.FailErr(t, "encode workers", err)

	exe, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	engine := exec.Command(exe, "-test.run=^TestRealMountKilledEngineRecoversThroughStartup$")
	engine.Env = append(os.Environ(), realEngineHelperEnv+"=1",
		"HELPER_WORKERS="+string(encoded), "HELPER_BOOT="+thisBoot(t), "HELPER_FOREIGN_ROOT="+foreignRoot, "HELPER_FOREIGN_RUNTIME="+foreignRuntime,
		"HELPER_PROJECT="+f.project, "HELPER_STORE="+filepath.Join(enginepaths.WorkerRecoveryRootUnder(f.stateRoot), "checkpoints"),
		"HELPER_STATE="+filepath.Join(enginepaths.WorkerRecoveryRootUnder(f.stateRoot), "state"))
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
		for _, w := range workers {
			mountedHost.detach(w.Root)
		}
		mountedHost.detach(foreignRoot)
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper engine not ready: %q err=%v", line, err)
	}

	holder := exec.Command("/bin/sh", "-c", `cd "$1" && echo inside; read _; exit 0`, "sh", workers[0].Root)
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
	for _, w := range workers {
		mountedHost.requireServerGone(t, w.Root)
		if err := workspace.RefuseMountedBranch(w.Root); !errors.Is(err, workspace.ErrBranchMounted) {
			t.Fatalf("a dead mount must refuse deletion: %v", err)
		}
	}
	mountedHost.requireServerGone(t, foreignRoot)

	coordinator, err := newWorkerRecovery(f.stateRoot, nil)
	testutil.FailErr(t, "production coordinator", err)
	next := f.boot()
	next.Recovery = coordinator
	// The restarted engine selects as production does, with view confinement enforced.
	confine.TestingSetAutoConfine(t)
	next.Workspace = workspace.NewManager(next.BranchRoot).WithRecovery(coordinator).WithSelector(workspace.EnforcedWorkspaceSelector())
	next.Queue.SetWorkerWorkspaceManager(next.Workspace)
	testutil.FailErr(t, "startup recovery", next.RecoverWorkerViews(t.Context()))

	for i, w := range workers {
		if err := workspace.RefuseMountedBranch(w.Root); err != nil {
			t.Fatalf("mount remains after recovery: %v", err)
		}
		if resumed, ok := next.Queue.Get(w.Task); !ok || resumed.Status.IsTerminal() {
			t.Fatalf("worker did not resume: %+v", resumed)
		}
		records, err := next.Recovery.ListInterruptedRecords(w.Task)
		testutil.FailErr(t, "interrupted", err)
		idents, err := next.Recovery.ListMountIdentities(w.Task)
		testutil.FailErr(t, "identities", err)
		if i == 0 {
			// Same boot: the view comes back under its own identity with everything the worker did.
			if got, want := f.readBranch(w.Root, "app.txt"), "unsealed-after-A-0\n"; got != want {
				t.Fatalf("resumed branch shows %q, want %q", got, want)
			}
			if got := f.readBranch(w.Root, "moved.txt"); got != "moved-0\n" {
				t.Fatalf("resumed branch lost a rename: %q", got)
			}
			if _, err := os.Stat(filepath.Join(w.Root, "extra.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a renamed name came back: %v", err)
			}
			if len(records) != 0 || len(idents) != 1 || idents[0].ViewID != w.Task {
				t.Fatalf("resumed worker: interrupted %+v identities %+v", records, idents)
			}
			continue
		}
		if got, want := f.readBranch(w.Root, "app.txt"), fmt.Sprintf("checkpoint-A-%d\n", i); got != want {
			t.Fatalf("rebuilt branch shows %q, want %q", got, want)
		}
		if len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, fmt.Sprintf("unsealed-after-A-%d\n", i)) {
			t.Fatalf("interrupted work: %+v", records)
		}
		if len(idents) != 1 || idents[0].ViewID == w.Task {
			t.Fatalf("killed view identity must be gone, rebuilt view recorded: %+v", idents)
		}
	}
	if mounted, err := adapter.MountBoundaryPresent(foreignRoot); err != nil || !mounted {
		t.Fatalf("startup touched a mount it never recorded: mounted=%v err=%v", mounted, err)
	}
	if err := holder.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("holder inside the mount was affected: %v", err)
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

// runRealEngine is the engine that dies: it serves a real mount per worker, publishes through
// each and leaves later work unsealed, and serves one more mount it records nowhere.
func runRealEngine(t *testing.T) {
	var workers []helperWorker
	testutil.FailErr(t, "helper workers", json.Unmarshal([]byte(os.Getenv("HELPER_WORKERS")), &workers))
	store, err := checkpoint.NewStore(os.Getenv("HELPER_STORE"))
	testutil.FailErr(t, "helper store", err)
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{RootDir: os.Getenv("HELPER_STATE"), CheckpointStore: store, CurrentBootID: os.Getenv("HELPER_BOOT")})
	testutil.FailErr(t, "helper coordinator", err)
	mountedHost := newRealEngineHost()
	adp := mountedHost.adapter
	prepare := func(viewID, root, runtime, backing string) *adapter.PreparedView {
		view, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
			ViewID: viewID, RuntimeDir: runtime, BackingDir: backing,
			TargetRootDirs: map[string]string{fixtureRootID: root},
			QuiesceHook:    mountedHost.quiesce, FlushClientHook: mountedHost.flushClients,
			Grants: []adapter.RootGrant{{RootID: fixtureRootID, Label: fixtureRootID, SourcePath: os.Getenv("HELPER_PROJECT"), IsPrimary: true}},
		})
		testutil.FailErr(t, "helper prepare", err)
		return view
	}
	for i, w := range workers {
		view := prepare(w.Task, w.Root, w.Runtime, w.Backing)
		// The first worker's engine exits in this boot; the second's journal was written in an earlier one.
		boot := os.Getenv("HELPER_BOOT")
		if i > 0 {
			boot = "an-earlier-boot"
		}
		testutil.FailErr(t, "helper journal", coordinator.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
			WorkerID: w.Task, ViewID: w.Task, TaskID: w.Task, ProjectID: w.Key, AdapterID: adp.ID(), BackingDir: view.BackingDir, BootID: boot,
		}))
		app := filepath.Join(w.Root, "app.txt")
		testutil.FailErr(t, "helper write", os.WriteFile(app, fmt.Appendf(nil, "checkpoint-A-%d\n", i), 0o644))
		publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
			Core: view.Core, Store: store, WorkerID: w.Task, TaskID: w.Task, ProjectID: w.Key, SourceGeneration: "1",
			SelectedRoots: []layeredfs.RootSpec{{ID: fixtureRootID, Label: fixtureRootID, TrunkPath: os.Getenv("HELPER_PROJECT"), IsPrimary: true}},
			Callbacks:     adp.NewBarrierProvider(w.Task),
		})
		testutil.FailErr(t, "helper publisher", err)
		checkpointID := fmt.Sprintf("cp-A-%d", i)
		_, err = publisher.Publish(context.Background(), checkpoint.PublishRequest{CheckpointID: checkpointID})
		testutil.FailErr(t, "helper publish", err)
		testutil.FailErr(t, "helper journal publish", coordinator.RecordTransition(w.Task, w.Task, recovery.PhasePublication, recovery.StateCommitted, checkpointID, "publish", nil, false))
		testutil.FailErr(t, "helper unsealed", os.WriteFile(app, fmt.Appendf(nil, "unsealed-after-A-%d\n", i), 0o644))
		extra := filepath.Join(w.Root, "extra.txt")
		testutil.FailErr(t, "helper create", os.WriteFile(extra, fmt.Appendf(nil, "moved-%d\n", i), 0o644))
		for _, name := range []string{app, extra} {
			file, err := os.Open(name)
			testutil.FailErr(t, "helper reopen", err)
			testutil.FailErr(t, "helper sync", file.Sync())
			_ = file.Close()
		}
		testutil.FailErr(t, "helper rename", os.Rename(extra, filepath.Join(w.Root, "moved.txt")))
	}
	foreignRuntime := os.Getenv("HELPER_FOREIGN_RUNTIME")
	prepare("foreign", os.Getenv("HELPER_FOREIGN_ROOT"), foreignRuntime, filepath.Join(foreignRuntime, "backing"))
	fmt.Println("ready")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

// thisBoot is the boot identity the production coordinator records, which an engine killed in this
// boot shares.
func thisBoot(t *testing.T) string {
	t.Helper()
	boot, ok := osprocess.BootID()
	if !ok {
		t.Fatal("this host reports no boot identity")
	}
	return boot
}

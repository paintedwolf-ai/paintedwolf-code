package delegations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// errSimulatedCrash stops a test's in-process work at a chosen boundary. Abandoning the objects
// without releasing them is process-crash evidence, not proof of physical power-loss behavior.
var errSimulatedCrash = errors.New("simulated process crash")

const fixtureRootID = "root-id"

var primaryPath = layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "app.txt"}

// connectionScript is the recovery the adapter contract would run against a real kernel. It
// checks the branch tree still exists when it is asked, which proves settlement came first.
type connectionScript struct {
	mu          sync.Mutex
	disposition adapter.ConnectionDisposition
	settled     []string
	onSettle    func(adapter.RecordedConnection)
}

func (s *connectionScript) SettleRecordedConnection(_ context.Context, rec adapter.RecordedConnection) (adapter.ConnectionSettlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settled = append(s.settled, rec.ViewID)
	if s.onSettle != nil {
		s.onSettle(rec)
	}
	return adapter.ConnectionSettlement{Disposition: s.disposition, Diagnostic: "scripted"}, nil
}

func (s *connectionScript) set(d adapter.ConnectionDisposition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disposition = d
}

func (s *connectionScript) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.settled...)
}

// engineFixture is one installation on disk; boot constructs a fresh engine process's objects
// over it, sharing nothing in memory with the one before.
type engineFixture struct {
	t         *testing.T
	database  *db.Store
	stateRoot string
	project   string
	script    *connectionScript
	writeHook func(dir, name string, raw []byte) error
	faults    recovery.FaultInjector
	engine    *Runtime
}

func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	project := t.TempDir()
	testutil.FailErr(t, "seed app", os.WriteFile(filepath.Join(project, "app.txt"), []byte("trunk\n"), 0o644))
	testutil.FailErr(t, "seed keep", os.WriteFile(filepath.Join(project, "keep.txt"), []byte("keep\n"), 0o644))
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", project)
	f := &engineFixture{
		t: t, database: database, project: project,
		stateRoot: testbaseline.DataDir(t, database),
		script:    &connectionScript{disposition: adapter.ConnectionAbsent},
	}
	f.boot()
	return f
}

func (f *engineFixture) boot() *Runtime {
	f.t.Helper()
	engine := New(f.database, nil, worker.WorkersConfig{})
	engine.BranchRoot = enginepaths.WorkerBranchesRootUnder(f.stateRoot)
	engine.SeedRoot = enginepaths.WorkerSeedsRootUnder(f.stateRoot)
	root := enginepaths.WorkerRecoveryRootUnder(f.stateRoot)
	store, err := checkpoint.NewStore(filepath.Join(root, "checkpoints"))
	testutil.FailErr(f.t, "open checkpoint store", err)
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir: filepath.Join(root, "state"), CheckpointStore: store, CurrentBootID: "test-boot",
		ProcessCtrl:     recovery.RecordedProcessController{},
		MountReconciler: recovery.ConnectionReconciler{Recoveries: map[adapter.AdapterID]adapter.ConnectionRecovery{adapter.AdapterFUSELinux: f.script}},
		WriteHook:       f.writeHook,
		FaultInjector:   f.faults,
	})
	testutil.FailErr(f.t, "open coordinator", err)
	engine.Recovery = coordinator
	engine.Workspace = workspace.NewManager(engine.BranchRoot, engine.SeedRoot).WithRecovery(coordinator)
	engine.Queue.SetWorkerWorkspaceManager(engine.Workspace)
	engine.Queue.SetBaselineStore(sourceledger.New(f.database, filepath.Join(f.stateRoot, "source-content")).Baselines)
	f.engine = engine
	return engine
}

// mountedSelector makes the ordinary claim path prepare a mounted view without a kernel mount.
func mountedSelector() *adapter.Selector {
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux, TargetOS: runtime.GOOS,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) { return adapter.ReasonNone, "", nil },
		MountViewFn:  func(context.Context, string, []adapter.PreparedRoot) error { return nil },
	})
	selector := adapter.NewSelector(runtime.GOOS, mounted, mounted, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.Confinement = fixtureConfinement{}
	return selector
}

// claimMountedWorker enters through the real queue and claim path and records the adapter's
// connection identity the way a platform adapter does, with its server already gone.
func (f *engineFixture) claimMountedWorker() *api.WorkerTask {
	f.t.Helper()
	ctx := f.t.Context()
	f.engine.Workspace.WithSelector(mountedSelector())
	id, err := f.engine.Queue.Enqueue(ctx, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.project, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "Change app.txt", Brief: "Change app.txt",
	})
	testutil.FailErr(f.t, "enqueue", err)
	claimed, err := f.engine.Queue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(f.t, "claim next", err)
	claimed, err = f.engine.Queue.ClaimWorkerBranch(ctx, claimed.ID)
	testutil.FailErr(f.t, "claim branch", err)
	if claimed.ID != id || claimed.WorkspaceRoot == "" {
		f.t.Fatalf("claimed %+v", claimed)
	}
	projectKey := filepath.Base(filepath.Dir(claimed.WorkspaceRoot))
	testutil.FailErr(f.t, "record connection", adapter.WriteRecordedConnection(
		filepath.Join(enginepaths.WorkerBranchesRootUnder(f.stateRoot), ".adapter", projectKey, claimed.ID),
		adapter.RecordedConnection{
			AdapterID: adapter.AdapterFUSELinux, ViewID: claimed.ID, RootID: fixtureRootID, MountPath: claimed.WorkspaceRoot,
			FSName: "lycaon-" + claimed.ID + "-" + fixtureRootID, DevMajorMinor: "0:999", Server: adapter.ProcessIdentity{PID: 1 << 30, Started: 1},
		}))
	return claimed
}

// liveView returns the view the ordinary claim recorded for this branch.
func (f *engineFixture) liveView(root string) *adapter.PreparedView {
	f.t.Helper()
	claimed, ok := workspace.LookupBranchClaimedView(root)
	if !ok || claimed.Prepared == nil {
		f.t.Fatalf("no claimed view for %s", root)
	}
	return claimed.Prepared
}

// publish captures view as a complete checkpoint, journaled as production publication is.
func (f *engineFixture) publish(task *api.WorkerTask, view *adapter.PreparedView, checkpointID string) {
	f.t.Helper()
	journalFromBranch(f, task)
	publisher := f.publisher(task, view)
	_, err := publisher.Publish(f.t.Context(), checkpoint.PublishRequest{CheckpointID: checkpointID})
	testutil.FailErr(f.t, "publish", err)
	testutil.FailErr(f.t, "record publication", f.engine.Recovery.RecordTransition(task.ID, view.ViewID, recovery.PhasePublication, recovery.StateCommitted, checkpointID, "publish", nil, false))
}

func (f *engineFixture) publisher(task *api.WorkerTask, view *adapter.PreparedView) *checkpoint.Publisher {
	f.t.Helper()
	publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: view.Core, Store: checkpointStore(f), WorkerID: task.ID, TaskID: task.ID,
		ProjectID: task.ProjectID, SourceGeneration: "1",
		SelectedRoots: []layeredfs.RootSpec{{ID: fixtureRootID, Label: fixtureRootID, TrunkPath: f.project, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			CommitRetention: func(ctx context.Context, id string, blobs, git []string) error {
				return f.engine.Recovery.CommitCheckpointRetention(ctx, task.ID, id, blobs, git, nil)
			},
		},
	})
	testutil.FailErr(f.t, "new publisher", err)
	return publisher
}

func checkpointStore(f *engineFixture) *checkpoint.Store {
	f.t.Helper()
	store, err := checkpoint.NewStore(filepath.Join(enginepaths.WorkerRecoveryRootUnder(f.stateRoot), "checkpoints"))
	testutil.FailErr(f.t, "open checkpoint store", err)
	return store
}

// journalFromBranch gives the worker a journal from its durable records, as startup would.
func journalFromBranch(f *engineFixture, task *api.WorkerTask) {
	f.t.Helper()
	if _, err := f.engine.Recovery.LoadWorkerJournal(task.ID); err == nil {
		return
	}
	testutil.FailErr(f.t, "adopt view", f.engine.Recovery.AdoptRecordedView(recovery.RecordedView{
		WorkerID: task.ID, ViewID: task.ID, TaskID: task.ID, ProjectID: task.ProjectID,
		AdapterID: adapter.AdapterFUSELinux, Mounted: true, BackingDir: f.liveView(task.WorkspaceRoot).BackingDir,
		RuntimeViewDir: f.liveView(task.WorkspaceRoot).RuntimeViewDir,
	}))
}

func (f *engineFixture) readBranch(root, rel string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	testutil.FailErr(f.t, "read branch "+rel, err)
	return string(raw)
}

func mutate(t *testing.T, view *adapter.PreparedView, content string) {
	t.Helper()
	_, err := view.Core.WriteFile(primaryPath, []byte(content), 0o644)
	testutil.FailErr(t, "write through view", err)
}

// claimDefaultWorker takes the ordinary claim path with this host's default selection.
func (f *engineFixture) claimDefaultWorker() *api.WorkerTask {
	f.t.Helper()
	ctx := f.t.Context()
	_, err := f.engine.Queue.Enqueue(ctx, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.project, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "Change app.txt", Brief: "Change app.txt",
	})
	testutil.FailErr(f.t, "enqueue", err)
	claimed, err := f.engine.Queue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(f.t, "claim next", err)
	claimed, err = f.engine.Queue.ClaimWorkerBranch(ctx, claimed.ID)
	testutil.FailErr(f.t, "claim branch", err)
	return claimed
}

// fixtureConfinement stands in for enforcement around the fixture adapter. These tests exercise
// recovery ordering, not denial.
type fixtureConfinement struct{}

func (fixtureConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (fixtureConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

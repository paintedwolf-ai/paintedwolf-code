package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

const publishOperation = "publish_checkpoint"

// layeredServices are the host services every view on this Manager's branch root shares.
type layeredServices struct {
	privateGit  *privategit.Manager
	recovery    *recovery.Coordinator
	retention   claim.RetentionRegistrar
	coordinator *claim.Coordinator
}

// durableRetention records view retention in the recovery coordinator's claims, which its garbage
// collection honors and which survive an engine restart.
type durableRetention struct{ claims *recovery.Coordinator }

func (r durableRetention) RegisterClaim(_ context.Context, workerID, viewID string, _ []string) (string, error) {
	claimID := "claim-view-" + viewID
	err := r.claims.AcquireClaim(recovery.RetentionClaim{
		ClaimID: claimID, Kind: recovery.ClaimActiveView, HolderID: workerID, WorkerID: workerID, ViewID: viewID,
	})
	return claimID, err
}

func (r durableRetention) ReleaseClaim(claimID string) error { return r.claims.ReleaseClaim(claimID) }

func (r durableRetention) HoldUnresolved(_ context.Context, workerID, viewID string) (string, error) {
	claimID := "claim-unsettled-" + viewID
	err := r.claims.AcquireClaim(recovery.RetentionClaim{
		ClaimID: claimID, Kind: recovery.ClaimUnresolvedSettlement, HolderID: workerID, WorkerID: workerID, ViewID: viewID,
	})
	return claimID, err
}

// OpenPrivateGit opens the private Git manager for a worker branch root.
func OpenPrivateGit(branchRoot string) (*privategit.Manager, error) {
	return privategit.NewManager(privategit.ManagerConfig{BackingDir: filepath.Join(branchRoot, adapterRuntimeDirName, "privategit")})
}

// WithPrivateGit shares the host's private Git manager, the one the Git tools consult, with this Manager.
func (m *Manager) WithPrivateGit(mgr *privategit.Manager) *Manager {
	m.privateGit = mgr
	return m
}

// layered returns the shared services, opening them under the branch root on first use.
func (m *Manager) layered() (*layeredServices, error) {
	m.layeredOnce.Do(func() {
		m.services, m.servicesErr = m.openLayeredServices()
	})
	return m.services, m.servicesErr
}

func (m *Manager) openLayeredServices() (*layeredServices, error) {
	if strings.TrimSpace(m.branchRoot) == "" {
		return nil, fmt.Errorf("worker branch root is unavailable")
	}
	stateDir := filepath.Join(m.branchRoot, adapterRuntimeDirName)
	gitManager := m.privateGit
	if gitManager == nil {
		var err error
		gitManager, err = OpenPrivateGit(m.branchRoot)
		if err != nil {
			return nil, err
		}
	}
	claims := m.recovery
	if claims == nil {
		recoveryDir := filepath.Join(stateDir, "recovery")
		store, err := checkpoint.NewStore(filepath.Join(recoveryDir, "checkpoints"))
		if err != nil {
			return nil, err
		}
		claims, err = recovery.NewCoordinator(recovery.CoordinatorConfig{
			RootDir: recoveryDir, CheckpointStore: store, GitStore: gitManager.Store(), GitManager: gitManager,
			ProcessCtrl: recovery.RecordedProcessController{},
		})
		if err != nil {
			return nil, err
		}
	}
	selector := m.selector
	if selector == nil {
		selector = DefaultWorkspaceSelector()
		m.selector = selector
	}
	return &layeredServices{
		privateGit: gitManager, recovery: claims, retention: durableRetention{claims: claims},
		coordinator: claim.NewCoordinator(selector),
	}, nil
}

// branchLayeredView is the live state a mounted branch holds besides its claim.
type branchLayeredView struct {
	engine    *consumer.Engine
	publisher *checkpoint.Publisher
	services  *layeredServices
	workerID  string
	viewID    string
}

// bindViewConsumer creates the view's one consumer Engine and its checkpoint publisher. The Engine
// registers itself for every path of the view, and its settlement unregisters all of them.
func bindViewConsumer(
	branchRoot string,
	cv *claim.ClaimedView,
	projectID string,
	tracker *observation.Tracker,
	services *layeredServices,
) (*branchLayeredView, error) {
	prepared := cv.Prepared
	store := services.recovery.CheckpointStore()
	if err := adoptClaimedView(cv, projectID, services.recovery); err != nil {
		return nil, err
	}
	roots := make([]layeredfs.RootSpec, 0, len(prepared.Roots))
	for _, r := range prepared.Roots {
		roots = append(roots, layeredfs.RootSpec{ID: r.RootID, Label: r.Label, TrunkPath: r.SourcePath, Subroot: r.Subroot, IsPrimary: r.IsPrimary})
	}
	tracker.BindCore(prepared.Core, roots...)
	if err := tracker.BindViewCapabilities(observation.ViewCapabilitiesFromPreparedView(prepared)); err != nil {
		return nil, err
	}
	engine, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID: prepared.ViewID, PreparedView: prepared, Adapter: cv.Adapter, Roots: roots,
		CheckpointStore: store, Tracker: tracker, Index: newCatalogSourceIndex(projectID, prepared.Roots),
	})
	if err != nil {
		return nil, err
	}
	consumer.RegisterBranchEngine(branchRoot, engine)
	publisher, err := newViewPublisher(cv, store, services, roots)
	if err != nil {
		consumer.UnregisterEngine(engine)
		return nil, err
	}
	return &branchLayeredView{engine: engine, publisher: publisher, services: services, workerID: cv.Identity.WorkerID, viewID: prepared.ViewID}, nil
}

// adoptClaimedView gives recovery the view's journal and identity before any process can enter it, so
// writers have a record to join and a later engine settles this view like any other.
func adoptClaimedView(cv *claim.ClaimedView, projectID string, coordinator *recovery.Coordinator) error {
	prepared := cv.Prepared
	connections, err := adapter.LoadRecordedConnections(prepared.RuntimeViewDir)
	if err != nil {
		return err
	}
	mountPaths := connectionMountPaths(connections)
	if len(mountPaths) == 0 {
		for _, root := range prepared.Roots {
			mountPaths = append(mountPaths, root.WorkspacePath)
		}
	}
	return coordinator.AdoptRecordedView(recovery.RecordedView{
		WorkerID: cv.Identity.WorkerID, ViewID: prepared.ViewID, TaskID: cv.Identity.WorkerID, ProjectID: projectID,
		AdapterID: cv.Adapter.ID(), Mounted: true, BackingDir: prepared.BackingDir, RuntimeViewDir: prepared.RuntimeViewDir,
		MountPaths: mountPaths, Connections: connections,
	})
}

// settleBranchLayeredView releases a mounted branch. Its Engine is withdrawn first so nothing new
// starts on the view, and restored if settlement fails, because the view is then still live.
func settleBranchLayeredView(ctx context.Context, branchRoot string, view *branchLayeredView) error {
	if view.engine == nil {
		return settleMaterializedBranchView(ctx, view)
	}
	consumer.UnregisterEngine(view.engine)
	err := view.services.recovery.SettleRecordedWriters(ctx, view.workerID, view.viewID)
	if err == nil {
		err = view.services.coordinator.ReleaseWorkerView(ctx, view.viewID, view.services.retention)
	}
	if err == nil {
		err = view.services.recovery.ForgetReleasedView(view.workerID, view.viewID)
	}
	if err != nil {
		consumer.RestoreEngine(view.engine, branchRoot)
		return err
	}
	return nil
}

// BranchProcessRecorder returns the launch hook naming each process started in branchRoot's mounted
// view. The view is looked up at each start because recovery can rebuild it on another adapter.
func BranchProcessRecorder(branchRoot string) func(pid int) error {
	clean := filepath.Clean(branchRoot)
	return func(pid int) error {
		raw, ok := sharedBranchViews.Load(clean)
		if !ok {
			return nil
		}
		st := raw.(branchViewState)
		if st.selection.Facts.PreparationKind != adapter.PreparationMounted || len(st.views) == 0 || st.views[0] == nil {
			return nil
		}
		if st.writers == nil {
			return fmt.Errorf("mounted view of %s has no recovery record for its writers", clean)
		}
		identity, err := adapter.CurrentProcessIdentity(pid)
		if err != nil {
			return err
		}
		return st.writers.RecordWorkerProcess(jobIDForBranch(clean), st.views[0].ViewID, identity)
	}
}

// PublishBranchCheckpoint publishes a branch's private changes under the adapter's barrier. It runs at
// worker completion, the boundary every view shares; only a mounted view has any other. handled is
// false for a branch of an earlier format, whose changes its baseline captures instead.
func PublishBranchCheckpoint(ctx context.Context, branchRoot string) (handled bool, err error) {
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if ok && rec.layered == nil && rec.reopen != nil {
		layered, err := rec.reopen(ctx)
		if err != nil {
			return true, fmt.Errorf("reopen materialized view: %w", err)
		}
		rec.layered = layered
		ok = true
	}
	if ok && rec.layered != nil {
		return true, publishLayeredCheckpoint(ctx, branchRoot, rec.layered)
	}
	if materializedBranchLostItsView(branchRoot) {
		// An empty baseline would seal the whole tree as this worker's change.
		return true, fmt.Errorf("worker branch %s has no view to capture through", branchRoot)
	}
	return false, nil
}

// publishLayeredCheckpoint journals the publication around the transaction: a crash between the two
// leaves a begun phase that recovery treats as interrupted, and only a committed transition names
// the checkpoint a worker can rebuild from. The branch metadata then points promotion at it.
func publishLayeredCheckpoint(ctx context.Context, branchRoot string, view *branchLayeredView) error {
	checkpointID := fmt.Sprintf("%s-%s", view.viewID, uuid.NewString()[:8])
	coordinator := view.services.recovery
	if err := coordinator.RecordTransition(view.workerID, view.viewID, recovery.PhasePublication, recovery.StateBegin, checkpointID, publishOperation, nil, false); err != nil {
		return err
	}
	result, err := publishWithPrivateGit(ctx, view, checkpointID)
	if err != nil {
		_, stateErr := view.services.coordinator.RecordCheckpointFailure(view.viewID, checkpointID, adapter.ReasonBarrierFailed, err.Error())
		return errors.Join(err, stateErr, coordinator.RecordTransition(view.workerID, view.viewID, recovery.PhasePublication, recovery.StateFailed, checkpointID, publishOperation, nil, false))
	}
	if err := coordinator.RecordTransition(view.workerID, view.viewID, recovery.PhasePublication, recovery.StateCommitted, checkpointID, publishOperation, nil, false); err != nil {
		return err
	}
	if _, err := view.services.coordinator.RecordCheckpointPublished(view.viewID, result.Manifest.CheckpointID); err != nil {
		return err
	}
	return recordBranchCheckpoint(branchRoot, coordinator.CheckpointStore(), result.Manifest.CheckpointID)
}

func recordBranchCheckpoint(branchRoot string, store *checkpoint.Store, checkpointID string) error {
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return err
	}
	meta.LastPublishedCheckpointID, meta.CheckpointStoreDir = checkpointID, store.RootDir()
	return WriteJobMeta(metaDir, meta)
}

func publishWithPrivateGit(ctx context.Context, view *branchLayeredView, checkpointID string) (checkpoint.PublishResult, error) {
	git, err := privateGitSnapshot(ctx, view.services.privateGit, view.viewID, checkpointID)
	if err != nil {
		return checkpoint.PublishResult{}, err
	}
	return view.publisher.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: checkpointID, Trigger: checkpoint.TriggerWorkerCompletion, PrivateGit: git,
	})
}

// BranchCheckpoint names the store and checkpoint a branch last published.
func BranchCheckpoint(branchRoot string) (storeDir, checkpointID string, ok bool) {
	clean := strings.TrimSpace(branchRoot)
	if clean == "" {
		return "", "", false
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(filepath.Clean(clean)))
	if err != nil || meta.CheckpointStoreDir == "" || meta.LastPublishedCheckpointID == "" {
		return "", "", false
	}
	return meta.CheckpointStoreDir, meta.LastPublishedCheckpointID, true
}

// privateGitSnapshot captures Git state for the checkpoint only once the repository was initialized:
// a view whose worker never touched Git has none, and capturing must not initialize it.
func privateGitSnapshot(ctx context.Context, mgr *privategit.Manager, viewID, checkpointID string) (checkpoint.PrivateGitSnapshot, error) {
	snapshot := checkpoint.PrivateGitSnapshot{}
	metrics, initialized := mgr.RepoMetrics(viewID, ".")
	if initialized && metrics.State == privategit.StateReady {
		state, err := mgr.CaptureCheckpointGitState(ctx, viewID, checkpointID)
		if err != nil {
			return checkpoint.PrivateGitSnapshot{}, err
		}
		snapshot.HeadRef, snapshot.Refs, snapshot.RetainedObjects = state.HeadSymbolic, state.Refs, state.Artifact.ReachableOIDs
	}
	for _, secondary := range mgr.SecondaryViews(viewID) {
		if metrics, initialized := mgr.RepoMetrics(secondary, "."); !initialized || metrics.State != privategit.StateReady {
			continue
		}
		state, err := mgr.CaptureCheckpointGitState(ctx, secondary, checkpointID+"-"+secondary)
		if err != nil {
			return checkpoint.PrivateGitSnapshot{}, err
		}
		snapshot.RetainedObjects = append(snapshot.RetainedObjects, state.Artifact.ReachableOIDs...)
	}
	return snapshot, nil
}

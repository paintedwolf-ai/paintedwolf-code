package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

// BranchCheckpointStoreSuffix names the sibling directory holding a branch's published checkpoints.
const BranchCheckpointStoreSuffix = ".lycaon-checkpoint-store"


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
	viewID    string
}

func branchCheckpointStoreDir(branchRoot string) string {
	return filepath.Clean(branchRoot) + BranchCheckpointStoreSuffix
}

// bindViewConsumer creates the view's one consumer Engine and its checkpoint publisher. The Engine
// registers itself for every path of the view, and its settlement unregisters all of them.
func bindViewConsumer(
	binding *Binding,
	cv *claim.ClaimedView,
	projectID string,
	tracker *observation.Tracker,
	services *layeredServices,
) (*branchLayeredView, error) {
	prepared := cv.Prepared
	store, err := checkpoint.NewStore(branchCheckpointStoreDir(binding.Root))
	if err != nil {
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
	consumer.RegisterBranchEngine(binding.Root, engine)
	publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prepared.Core, Store: store, WorkerID: cv.Identity.WorkerID, TaskID: cv.Identity.WorkerID,
		ProjectID: cv.Identity.ProjectID, SourceGeneration: fmt.Sprint(cv.Identity.SourceGeneration), SelectedRoots: roots,
		Callbacks: checkpoint.BarrierCallbacks{
			AcquireAdapterBarrier: func(ctx context.Context) (layeredfs.BarrierToken, func() error, error) {
				receipt, release, err := cv.Adapter.CheckpointBarrier(ctx, prepared.ViewID)
				if err != nil {
					return layeredfs.BarrierToken{}, nil, err
				}
				return receipt.Token, release, nil
			},
		},
	})
	if err != nil {
		consumer.UnregisterEngine(engine)
		return nil, err
	}
	return &branchLayeredView{engine: engine, publisher: publisher, services: services, viewID: prepared.ViewID}, nil
}

// settleBranchLayeredView releases a mounted branch. Its Engine is withdrawn first so nothing new
// starts on the view, and restored if settlement fails, because the view is then still live.
func settleBranchLayeredView(ctx context.Context, branchRoot string, view *branchLayeredView) error {
	consumer.UnregisterEngine(view.engine)
	err := view.services.coordinator.ReleaseWorkerView(ctx, view.viewID, view.services.retention)
	if err != nil {
		consumer.RestoreEngine(view.engine, branchRoot)
		return err
	}
	return nil
}

// PublishBranchCheckpoint publishes the mounted branch's private changes under the adapter's barrier.
// handled is false for a branch with no mounted view, whose changes a baseline captures instead.
func PublishBranchCheckpoint(ctx context.Context, branchRoot string) (handled bool, err error) {
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if !ok || rec.layered == nil {
		return false, nil
	}
	view := rec.layered
	checkpointID := fmt.Sprintf("%s-completion", view.viewID)
	git, err := privateGitSnapshot(ctx, view.services.privateGit, view.viewID, checkpointID)
	if err != nil {
		return true, err
	}
	result, err := view.publisher.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: checkpointID, Trigger: checkpoint.TriggerExplicitCapture, PrivateGit: git,
	})
	if err != nil {
		_, _ = view.services.coordinator.RecordCheckpointFailure(view.viewID, checkpointID, adapter.ReasonBarrierFailed, err.Error())
		return true, err
	}
	_, _ = view.services.coordinator.RecordCheckpointPublished(view.viewID, result.Manifest.CheckpointID)
	return true, nil
}

// privateGitSnapshot captures Git state for the checkpoint only once the repository was initialized:
// a view whose worker never touched Git has none, and capturing must not initialize it.
func privateGitSnapshot(ctx context.Context, mgr *privategit.Manager, viewID, checkpointID string) (checkpoint.PrivateGitSnapshot, error) {
	metrics, initialized := mgr.RepoMetrics(viewID, ".")
	if !initialized || metrics.State != privategit.StateReady {
		return checkpoint.PrivateGitSnapshot{}, nil
	}
	state, err := mgr.CaptureCheckpointGitState(ctx, viewID, checkpointID)
	if err != nil {
		return checkpoint.PrivateGitSnapshot{}, err
	}
	return checkpoint.PrivateGitSnapshot{
		HeadRef: state.HeadSymbolic, Refs: state.Refs,
		RetainedObjects: state.Artifact.ReachableOIDs,
	}, nil
}

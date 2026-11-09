package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	workerClaimStateMu sync.RWMutex
	workerClaimStates  = make(map[string]claim.WorkspaceStateSnapshot)
)

func recordWorkerClaimState(jobID string, snap claim.WorkspaceStateSnapshot) {
	workerClaimStateMu.Lock()
	workerClaimStates[strings.TrimSpace(jobID)] = snap
	workerClaimStateMu.Unlock()
}

// WorkerClaimWorkspaceState returns the typed WorkspaceStateSnapshot and DispatchWorkspaceReceipt for jobID (#1662).
func WorkerClaimWorkspaceState(jobID string) (claim.WorkspaceStateSnapshot, claim.DispatchWorkspaceReceipt, bool) {
	workerClaimStateMu.RLock()
	snap, ok := workerClaimStates[strings.TrimSpace(jobID)]
	workerClaimStateMu.RUnlock()
	if !ok {
		return claim.WorkspaceStateSnapshot{}, claim.DispatchWorkspaceReceipt{}, false
	}
	return snap, claim.BuildReceipt(snap), true
}

const workspacePreparationEmitInterval = 500 * time.Millisecond

// ErrWorkerBranchClaimFailed is returned when a write worker cannot get an isolated sandbox.
var ErrWorkerBranchClaimFailed = fmt.Errorf("worker branch claim failed")

var workerWorkspaceLog = observability.LazyComponent("worker_workspace")

// WorkerWorkspaceManager provisions isolated sandbox copies for write workers.
type WorkerWorkspaceManager interface {
	CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID string) (*workspace.Binding, workspace.SandboxLayout, error)
	CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID, parentCheckpointID string) (*workspace.Binding, workspace.SandboxLayout, error)
	PublishSealedOverlayCheckpoint(ctx context.Context, sealed workspace.SealedOverlay) (string, error)
	CheckpointCoordinator() (*recovery.Coordinator, error)
	RebuildWorkerWorkspace(ctx context.Context, branchRoot string, restore func(ctx context.Context, layout workspace.SandboxLayout, branchRoot string) error) (workspace.SandboxLayout, error)
	DestroyWorkerWorkspace(binding *workspace.Binding) error
}

// ProjectStore loads project metadata for worker sandbox provisioning.
type ProjectStore interface {
	Get(ctx context.Context, projectID string) (*project.Project, error)
}

func (q *SQLQueue) ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, ErrWorkerBranchClaimFailed
	}
	release, err := q.branchClaims.Acquire(ctx, jobID)
	if err != nil {
		return nil, err
	}
	defer release()
	task, ok := q.Get(jobID) //nolint:contextcheck // bounded Get
	if !ok || task == nil {
		return nil, ErrWorkerBranchClaimFailed
	}
	for _, id := range task.AfterWorkers {
		upstream, ok := q.Get(id) //nolint:contextcheck // bounded Get
		if !ok || !api.WorkerOutputReady(upstream) {
			return nil, ErrWorkerPrerequisitesPending
		}
	}
	if !task.EffectiveScope().IsWrite() {
		return task, nil
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		return task, nil
	}
	claimCtx := withWorkspacePreparation(q, ctx, task)
	if err := provisionWorkerBranch(q, claimCtx, task); err != nil {
		clearWorkspacePreparation(q, task)
		return nil, err
	}
	clearWorkspacePreparation(q, task)
	q.refreshBoard(ctx, *task, task.ProjectID)
	return task, nil
}

func provisionWorkerBranch(q *SQLQueue, ctx context.Context, task *api.WorkerTask) error {
	if task == nil || !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) != "" {
		return nil
	}
	q.mu.Lock()
	ws, projects, baselines := q.workerWorkspace, q.projects, q.baselines
	q.mu.Unlock()
	if ws == nil || baselines == nil {
		return ErrWorkerBranchClaimFailed
	}
	roots := TaskRootRefs(ctx, task, projects)
	if len(roots) == 0 {
		return ErrWorkerBranchClaimFailed
	}
	parentCheckpoint, err := stackedParentCheckpoint(ctx, task, roots, q, ws)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	activeID := strings.TrimSpace(task.WorkspaceRootID)
	if task.Scope != nil && len(task.Scope.Paths) > 0 {
		ctx = workspace.WithWorkspaceSelection(ctx, adapter.WorkspaceSelection{}, task.Scope.Paths...)
	}
	ctx = workspace.WithWorkspaceProject(ctx, task.ProjectID)
	binding, layout, err := createTaskBranch(ctx, ws, roots, activeID, task.ID, parentCheckpoint)
	if err != nil {
		readiness := claim.StateFailedPreparation
		if errors.Is(err, claim.ErrSettlementUnresolved) {
			readiness = claim.StateRecoveryRequired
		}
		recordWorkerClaimState(task.ID, claim.WorkspaceStateSnapshot{
			Identity:  claim.ClaimIdentity{WorkerID: task.ID, ProjectID: task.ProjectID, ViewID: task.ID},
			Readiness: readiness,
			Notice:    claim.FallbackNotice{Severity: claim.SeverityError, ReasonCode: claim.ClassifyFailureReason(err), Diagnostic: err.Error()},
		})
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return ErrWorkerBranchClaimFailed
	}
	// A view captured by checkpoint keeps nothing here: hashing the tree is what claiming must not do.
	baselinePath := ""
	if !workspace.BranchUsesCheckpoints(binding.Root) {
		var err error
		baselinePath, err = baselines.Capture(ctx, task.ID, workspacebaseline.Branch(layout.Roots, binding.Root))
		if err != nil {
			return fmt.Errorf("%w: snapshot workspace: %w", ErrWorkerBranchClaimFailed, err)
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = baselines.Discard(cleanupCtx, baselinePath)
		}()
	}
	won, err := q.store.SetWorkerWorkspace(ctx, task.ID, binding.Root, baselinePath)
	if err != nil {
		return err
	}
	if !won {
		stored, ok := q.store.getTask(ctx, task.ID)
		if !ok || stored == nil || strings.TrimSpace(stored.WorkspaceRoot) == "" {
			return ErrWorkerBranchClaimFailed
		}
		if filepath.Clean(stored.WorkspaceRoot) != filepath.Clean(binding.Root) {
			if err := ws.DestroyWorkerWorkspace(binding); err != nil {
				workerWorkspaceLog.Warn("losing worker workspace left in custody", "job_id", task.ID, "error", err)
			}
		}
		task.WorkspaceRoot, task.WorkspaceBaselinePath = stored.WorkspaceRoot, stored.WorkspaceBaselinePath
	} else {
		task.WorkspaceRoot, task.WorkspaceBaselinePath = binding.Root, baselinePath
	}
	if snap, _, ok := workspace.BranchWorkspaceState(task.WorkspaceRoot); ok {
		recordWorkerClaimState(task.ID, snap)
	}
	return nil
}

func withWorkspacePreparation(q *SQLQueue, ctx context.Context, task *api.WorkerTask) context.Context {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return ctx
	}
	var reportMu sync.Mutex
	var lastEmit time.Time
	lastStage := ""
	return workspace.WithPreparationReporter(ctx, func(progress workspace.PreparationProgress) {
		reportMu.Lock()
		defer reportMu.Unlock()
		preparation := &api.WorkspacePreparation{
			Strategy: api.WorkspaceProvisionStrategy(progress.Strategy), Stage: progress.Stage,
			Files: progress.Files, Bytes: progress.Bytes, TotalBytes: progress.TotalBytes,
		}
		now := time.Now()
		emit := progress.Stage != lastStage || now.Sub(lastEmit) >= workspacePreparationEmitInterval
		q.mu.Lock()
		q.preparations[task.ID], task.WorkspacePreparation = preparation, preparation
		snapshot := *task
		q.mu.Unlock()
		if emit {
			lastEmit, lastStage = now, progress.Stage
			q.refreshBoard(context.WithoutCancel(ctx), snapshot, task.ProjectID)
		}
	})
}

func clearWorkspacePreparation(q *SQLQueue, task *api.WorkerTask) {
	if task == nil {
		return
	}
	q.mu.Lock()
	delete(q.preparations, task.ID)
	task.WorkspacePreparation = nil
	q.mu.Unlock()
}

func (q *SQLQueue) decorateWorkspacePreparations(tasks []api.WorkerTask) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range tasks {
		tasks[i].WorkspacePreparation = q.preparations[tasks[i].ID]
	}
}

// overlayDivergesFromProject checks the full branch against the project.
func (q *SQLQueue) overlayDivergesFromProject(ctx context.Context, task *api.WorkerTask) bool {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return false
	}
	q.mu.Lock()
	projects := q.projects
	q.mu.Unlock()
	return len(session.OverlayWorkspaceDivergencePaths(task, TaskRootRefs(ctx, task, projects))) > 0
}

func (q *SQLQueue) destroyWorkerWorkspace(_ context.Context, task *api.WorkerTask) {
	if task == nil || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if ws == nil {
		return
	}
	if err := ws.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}); err != nil {
		workerWorkspaceLog.Warn("worker workspace left in custody", "job_id", task.ID, "error", err)
	}
}

// captureOverlay seals a completing write worker's changes so its branch tree
// can be reclaimed and rebuilt while the overlay waits for review.
func (q *SQLQueue) captureOverlay(ctx context.Context, task *api.WorkerTask) (string, error) {
	q.mu.Lock()
	baselines, projects := q.baselines, q.projects
	q.mu.Unlock()
	if baselines == nil {
		return "", fmt.Errorf("worker %s: overlay capture requires a baseline store", task.ID)
	}
	captured, err := baselines.CaptureOverlay(ctx, task.ID, task.WorkspaceBaselinePath, TaskRootRefs(ctx, task, projects), task.WorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("worker %s: capture overlay: %w", task.ID, err)
	}
	workerWorkspaceLog.Info("sealed write overlay", "job_id", task.ID, "changed", captured.Changed, "deleted", captured.Deleted)
	return captured.Path, nil
}

// discardOverlay drops a capture the completion did not publish.
func (q *SQLQueue) discardOverlay(ctx context.Context, overlayPath string) {
	if overlayPath == "" {
		return
	}
	q.mu.Lock()
	baselines := q.baselines
	q.mu.Unlock()
	if baselines != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = baselines.Discard(cleanupCtx, overlayPath)
	}
}

// EnsureWorkerBranch hands back the job's branch tree, rebuilding it first
// when retention reclaimed it.
func (q *SQLQueue) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error) {
	task, ok := q.Get(strings.TrimSpace(jobID)) //nolint:contextcheck // bounded Get
	if !ok || task == nil {
		return nil, nil, fmt.Errorf("%w: job %q not found", ErrWorkerBranchUnavailable, jobID)
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	lease, err := ensureWorkerBranch(ctx, task, ws)
	if err != nil {
		return nil, nil, err
	}
	return task, lease, nil
}

func createTaskBranch(ctx context.Context, ws WorkerWorkspaceManager, roots []projectroot.RootRef, activeID, jobID, parentCheckpoint string) (*workspace.Binding, workspace.SandboxLayout, error) {
	if parentCheckpoint == "" {
		return ws.CreateWorkerBranch(ctx, roots, activeID, jobID)
	}
	return ws.CreateStackedWorkerBranch(ctx, roots, activeID, jobID, parentCheckpoint)
}

// ListBranchJobs reports every job that still names a branch tree.
func (q *SQLQueue) ListBranchJobs(ctx context.Context) ([]BranchJob, error) {
	rows, err := q.store.queries.ListWorkerJobsForBranchRetention(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BranchJob, 0, len(rows))
	for _, row := range rows {
		status := api.WorkerStatus(row.Status)
		mergeStatus := api.WorkerMergeStatus(db.StringFromNull(row.MergeStatus))
		overlayID := db.StringFromNull(row.WorkspaceOverlayID)
		var branchRoot string
		if status == api.WorkerStatusComplete && strings.TrimSpace(overlayID) == "" {
			if task, ok := q.store.getTask(ctx, row.ID); ok && task != nil {
				branchRoot = task.WorkspaceRoot
			}
		}
		out = append(out, BranchJob{
			ID: row.ID, ProjectID: row.ProjectID,
			Sealed: branchJobSealed(status, mergeStatus, overlayID, branchRoot),
		})
	}
	return out, nil
}

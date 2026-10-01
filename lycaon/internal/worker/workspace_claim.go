package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

const workspacePreparationEmitInterval = 500 * time.Millisecond

// ErrWorkerBranchClaimFailed is returned when a write worker cannot get an isolated sandbox.
var ErrWorkerBranchClaimFailed = fmt.Errorf("worker branch claim failed")

var workerWorkspaceLog = observability.LazyComponent("worker_workspace")

// WorkerWorkspaceManager provisions isolated sandbox copies for write workers.
type WorkerWorkspaceManager interface {
	CreateWorkerWorkspaceFromSources(ctx context.Context, roots, sourceRoots []projectroot.RootRef, activeRootID, jobID string) (*workspace.Binding, workspace.SandboxLayout, error)
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
	claimCtx := q.withWorkspacePreparation(ctx, task)
	if err := q.provisionWorkerBranch(claimCtx, task); err != nil {
		q.clearWorkspacePreparation(task)
		return nil, err
	}
	q.clearWorkspacePreparation(task)
	q.refreshBoard(ctx, *task, task.ProjectID)
	return task, nil
}

func (q *SQLQueue) provisionWorkerBranch(ctx context.Context, task *api.WorkerTask) error {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return nil
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		return nil
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	projects := q.projects
	baselines := q.baselines
	q.mu.Unlock()
	if ws == nil || baselines == nil {
		return ErrWorkerBranchClaimFailed
	}
	roots := TaskRootRefs(ctx, task, projects)
	if len(roots) == 0 {
		return ErrWorkerBranchClaimFailed
	}
	baseLease, err := q.holdBaseOverlay(ctx, task)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	defer baseLease.Release()
	sources, err := workspaceSourceRoots(task, roots, q)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	activeID := strings.TrimSpace(task.WorkspaceRootID)
	binding, layout, err := ws.CreateWorkerWorkspaceFromSources(ctx, roots, sources, activeID, task.ID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return ErrWorkerBranchClaimFailed
	}
	baselinePath, err := baselines.Capture(ctx, task.ID, workspacebaseline.Branch(layout.Roots, binding.Root))
	if err != nil {
		return fmt.Errorf("%w: snapshot workspace: %w", ErrWorkerBranchClaimFailed, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = baselines.Discard(cleanupCtx, baselinePath)
	}()
	// The store decides which completed capture becomes the job baseline.
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
			_ = ws.DestroyWorkerWorkspace(binding)
		}
		task.WorkspaceRoot = stored.WorkspaceRoot
		task.WorkspaceBaselinePath = stored.WorkspaceBaselinePath
		return nil
	}
	task.WorkspaceRoot = binding.Root
	task.WorkspaceBaselinePath = baselinePath
	return nil
}

func (q *SQLQueue) withWorkspacePreparation(ctx context.Context, task *api.WorkerTask) context.Context {
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
			Strategy:   api.WorkspaceProvisionStrategy(progress.Strategy),
			Stage:      progress.Stage,
			Files:      progress.Files,
			Bytes:      progress.Bytes,
			TotalBytes: progress.TotalBytes,
		}
		now := time.Now()
		emit := progress.Stage != lastStage || now.Sub(lastEmit) >= workspacePreparationEmitInterval
		q.mu.Lock()
		q.preparations[task.ID] = preparation
		task.WorkspacePreparation = preparation
		snapshot := *task
		q.mu.Unlock()
		if emit {
			lastEmit = now
			lastStage = progress.Stage
			q.refreshBoard(context.WithoutCancel(ctx), snapshot, task.ProjectID)
		}
	})
}

func (q *SQLQueue) clearWorkspacePreparation(task *api.WorkerTask) {
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
	roots := TaskRootRefs(ctx, task, projects)
	return len(session.OverlayWorkspaceDivergencePaths(task, roots)) > 0
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
	_ = ws.DestroyWorkerWorkspace(&workspace.Binding{
		ID:   task.ID,
		Root: task.WorkspaceRoot,
	})
}

// captureOverlay seals a completing write worker's changes so its branch tree
// can be reclaimed and rebuilt while the overlay waits for review.
func (q *SQLQueue) captureOverlay(ctx context.Context, task *api.WorkerTask) (string, error) {
	q.mu.Lock()
	baselines := q.baselines
	projects := q.projects
	q.mu.Unlock()
	if baselines == nil {
		return "", fmt.Errorf("worker %s: overlay capture requires a baseline store", task.ID)
	}
	// A branch with no recorded topology is a single-root tree.
	roots := TaskRootRefs(ctx, task, projects)
	captured, err := baselines.CaptureOverlay(ctx, task.ID, task.WorkspaceBaselinePath, roots, task.WorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("worker %s: capture overlay: %w", task.ID, err)
	}
	workerWorkspaceLog.Info("sealed write overlay", "job_id", task.ID,
		"changed", captured.Changed, "deleted", captured.Deleted)
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
	if baselines == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = baselines.Discard(cleanupCtx, overlayPath)
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

// holdBaseOverlay keeps a stacked worker's base branch on disk while the new
// branch clones from it. A worker without a base holds nothing.
func (q *SQLQueue) holdBaseOverlay(ctx context.Context, task *api.WorkerTask) (*BranchLease, error) {
	baseID := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseID == "" {
		return nil, nil
	}
	base, ok := q.Get(baseID) //nolint:contextcheck // bounded Get
	if !ok || base == nil || base.MergeStatus == api.WorkerMergeStatusMerged || strings.TrimSpace(base.WorkspaceRoot) == "" {
		// workspaceSourceRoots reports the precise lineage error.
		return nil, nil
	}
	_, lease, err := q.EnsureWorkerBranch(ctx, baseID)
	if err != nil {
		return nil, fmt.Errorf("base overlay %q: %w", baseID, err)
	}
	return lease, nil
}

// ListBranchJobs reports every job that still names a branch tree.
func (q *SQLQueue) ListBranchJobs(ctx context.Context) ([]BranchJob, error) {
	rows, err := q.store.queries.ListWorkerJobsForBranchRetention(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BranchJob, 0, len(rows))
	for _, row := range rows {
		mergeStatus := api.WorkerMergeStatus(db.StringFromNull(row.MergeStatus))
		out = append(out, BranchJob{
			ID: row.ID, ProjectID: row.ProjectID,
			Sealed: branchJobSealed(api.WorkerStatus(row.Status), mergeStatus, db.StringFromNull(row.WorkspaceOverlayID)),
		})
	}
	return out, nil
}

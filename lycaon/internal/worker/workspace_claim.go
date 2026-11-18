package worker

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/workerops"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
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
	CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID string) (*workspace.Binding, workspace.SandboxLayout, error)
	CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, jobID, parentCheckpointID string) (*workspace.Binding, workspace.SandboxLayout, error)
	PublishSealedOverlayCheckpoint(ctx context.Context, sealed workspace.SealedOverlay) (string, error)
	CheckpointCoordinator() (*recovery.Coordinator, error)
	RebuildWorkerWorkspace(ctx context.Context, branchRoot string, restore func(ctx context.Context, layout workspace.SandboxLayout, branchRoot string) error) (workspace.SandboxLayout, error)
	DestroyWorkerWorkspace(ctx context.Context, binding *workspace.Binding) error
}

// ProjectStore loads project metadata for worker sandbox provisioning.
type ProjectStore interface {
	Get(ctx context.Context, projectID string) (*project.Project, error)
}

func (b *BranchService) ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, ErrWorkerBranchClaimFailed
	}
	release, err := b.branchClaims.Acquire(ctx, jobID)
	if err != nil {
		return nil, err
	}
	defer release()
	task, ok := b.store.getTask(ctx, jobID)
	if !ok || task == nil {
		return nil, ErrWorkerBranchClaimFailed
	}
	if pending, err := workerops.PrerequisitePending(ctx, b.store.queries.LatestRoundJobForJob, func(id string) (*api.WorkerTask, bool) { return b.store.getTask(ctx, id) }, *task); err != nil {
		return nil, err
	} else if pending {
		return nil, ErrWorkerPrerequisitesPending
	}
	if !task.EffectiveScope().IsWrite() && !b.ReviewsWorker(ctx, task) {
		return task, nil
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		return task, nil
	}
	claimCtx, stopPreparation := b.withWorkspacePreparation(ctx, task)
	defer stopPreparation()
	if err := b.provisionWorkerBranch(claimCtx, task); err != nil {
		return nil, err
	}
	stopPreparation()
	b.changes.board(ctx, *task, task.ProjectID)
	return task, nil
}

// ReviewsWorker reports a read job that reviews a sealed result: it runs in
// a view of that result, prepared like a lane.
func (b *BranchService) ReviewsWorker(ctx context.Context, task *api.WorkerTask) bool {
	b.mu.Lock()
	lanes := b.lanes
	b.mu.Unlock()
	return lanes != nil && lanes.Reviews(ctx, task)
}

func (b *BranchService) provisionWorkerBranch(ctx context.Context, task *api.WorkerTask) error {
	if task == nil || strings.TrimSpace(task.WorkspaceRoot) != "" {
		return nil
	}
	reviewer := b.ReviewsWorker(ctx, task)
	if !task.EffectiveScope().IsWrite() && !reviewer {
		return nil
	}
	b.mu.Lock()
	ws, projects, baselines, lanes := b.workerWorkspace, b.projects, b.baselines, b.lanes
	b.mu.Unlock()
	if ws == nil || baselines == nil {
		return ErrWorkerBranchClaimFailed
	}
	roots := TaskRootRefs(ctx, task, projects)
	if len(roots) == 0 {
		return ErrWorkerBranchClaimFailed
	}
	if task.Scope != nil && len(task.Scope.Paths) > 0 {
		ctx = workspace.WithWorkspaceSelection(ctx, adapter.WorkspaceSelection{}, task.Scope.Paths...)
	}
	ctx = workspace.WithWorkspaceProject(ctx, task.ProjectID)
	if lanes != nil {
		root, ok, err := lanes.Prepare(ctx, task, roots)
		if ok {
			if err != nil {
				return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
			}
			return b.bindWorkerBranch(ctx, task, &workspace.Binding{ID: task.ID, Root: root}, workspace.SandboxLayout{}, baselines, ws, false)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
		}
	}
	if reviewer {
		// A reviewer has no view but its subject's.
		return fmt.Errorf("%w: review view unavailable", ErrWorkerBranchClaimFailed)
	}
	parentCheckpoint, err := stackedParentCheckpoint(ctx, task, roots, b.store.getTask, ws)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	activeID := strings.TrimSpace(task.WorkspaceRootID)
	binding, layout, err := createTaskBranch(ctx, ws, roots, activeID, task.ID, parentCheckpoint)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return ErrWorkerBranchClaimFailed
	}
	return b.bindWorkerBranch(ctx, task, binding, layout, baselines, ws, true)
}

// bindWorkerBranch records the branch a job runs in. A branch the job
// created is released if another claim recorded one first; a worker's lane
// outlives the job and is never released here.
func (b *BranchService) bindWorkerBranch(ctx context.Context, task *api.WorkerTask, binding *workspace.Binding, layout workspace.SandboxLayout, baselines *workspacebaseline.Store, ws WorkerWorkspaceManager, owned bool) error {
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return ErrWorkerBranchClaimFailed
	}
	// A view captured by checkpoint keeps nothing here: hashing the tree is what claiming must not do.
	baselinePath := ""
	if !workspace.BranchUsesCheckpoints(binding.Root) {
		if !owned {
			roots, err := workspace.BranchRootRefs(binding.Root)
			if err != nil {
				return fmt.Errorf("%w: lane roots: %w", ErrWorkerBranchClaimFailed, err)
			}
			layout = workspace.SandboxLayout{Roots: roots}
		}
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
	won, err := b.store.SetWorkerWorkspace(ctx, task.ID, binding.Root, baselinePath)
	if err != nil {
		return err
	}
	if !won {
		if !owned {
			binding = &workspace.Binding{ID: task.ID}
		}
		if err := b.adoptExistingWorkerWorkspace(ctx, task, binding, ws); err != nil {
			return err
		}
	} else {
		task.WorkspaceRoot, task.WorkspaceBaselinePath = binding.Root, baselinePath
	}
	return nil
}

// adoptExistingWorkerWorkspace takes the branch another claim recorded first and releases this one.
func (b *BranchService) adoptExistingWorkerWorkspace(ctx context.Context, task *api.WorkerTask, binding *workspace.Binding, ws WorkerWorkspaceManager) error {
	stored, ok := b.store.getTask(ctx, task.ID)
	if !ok || stored == nil || strings.TrimSpace(stored.WorkspaceRoot) == "" {
		return ErrWorkerBranchClaimFailed
	}
	if filepath.Clean(stored.WorkspaceRoot) != filepath.Clean(binding.Root) {
		if err := ws.DestroyWorkerWorkspace(context.WithoutCancel(ctx), binding); err != nil {
			workerWorkspaceLog.Warn("losing worker workspace left in custody", "job_id", task.ID, "error", err)
		}
	}
	task.WorkspaceRoot, task.WorkspaceBaselinePath = stored.WorkspaceRoot, stored.WorkspaceBaselinePath
	return nil
}

func (b *BranchService) withWorkspacePreparation(ctx context.Context, task *api.WorkerTask) (context.Context, func()) {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return ctx, func() {}
	}
	var reportMu sync.Mutex
	var lastEmit time.Time
	lastStage := ""
	stopped := false
	stop := func() {
		reportMu.Lock()
		defer reportMu.Unlock()
		if stopped {
			return
		}
		stopped = true
		b.mu.Lock()
		delete(b.preparations, task.ID)
		task.WorkspacePreparation = nil
		b.mu.Unlock()
	}
	return workspace.WithPreparationReporter(ctx, func(progress workspace.PreparationProgress) {
		reportMu.Lock()
		defer reportMu.Unlock()
		if stopped {
			return
		}
		preparation := &api.WorkspacePreparation{
			Strategy:          api.WorkspaceProvisionStrategy(progress.Strategy),
			Stage:             progress.Stage,
			Files:             progress.Files,
			Bytes:             progress.Bytes,
			TotalBytes:        progress.TotalBytes,
			TotalEntries:      progress.TotalEntries,
			UnreadableEntries: progress.UnreadableEntries,
			NotClonableReason: api.WorkspaceNotClonableReason(progress.NotClonableReason),
		}
		now := time.Now()
		emit := progress.Stage != lastStage || now.Sub(lastEmit) >= workspacePreparationEmitInterval
		b.mu.Lock()
		b.preparations[task.ID], task.WorkspacePreparation = preparation, preparation
		snapshot := *task
		b.mu.Unlock()
		if emit {
			lastEmit, lastStage = now, progress.Stage
			b.changes.board(context.WithoutCancel(ctx), snapshot, task.ProjectID)
		}
	}), stop
}

func (b *BranchService) decorateWorkspacePreparations(tasks []api.WorkerTask) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range tasks {
		tasks[i].WorkspacePreparation = b.preparations[tasks[i].ID]
	}
}

// overlayDivergesFromProject checks the full branch against the project.
func (b *BranchService) overlayDivergesFromProject(ctx context.Context, task *api.WorkerTask) bool {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return false
	}
	b.mu.Lock()
	projects := b.projects
	b.mu.Unlock()
	return len(session.OverlayWorkspaceDivergencePaths(task, TaskRootRefs(ctx, task, projects))) > 0 //nolint:contextcheck // The divergence summary reads the core, which takes no context.
}

func (b *BranchService) destroyWorkerWorkspace(ctx context.Context, task *api.WorkerTask) {
	if task == nil || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return
	}
	b.mu.Lock()
	ws, lanes := b.workerWorkspace, b.lanes
	b.mu.Unlock()
	if ws == nil || lanes.Holds(ctx, task) {
		return
	}
	if err := ws.DestroyWorkerWorkspace(context.WithoutCancel(ctx), &workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}); err != nil {
		workerWorkspaceLog.Warn("worker workspace left in custody", "job_id", task.ID, "error", err)
	}
}

// captureOverlay seals a completing write worker's changes so its branch tree
// can be reclaimed and rebuilt while the overlay waits for review.
func (b *BranchService) captureOverlay(ctx context.Context, task *api.WorkerTask) (string, error) {
	b.mu.Lock()
	baselines, projects := b.baselines, b.projects
	b.mu.Unlock()
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
func (b *BranchService) discardOverlay(ctx context.Context, overlayPath string) {
	if overlayPath == "" {
		return
	}
	b.mu.Lock()
	baselines := b.baselines
	b.mu.Unlock()
	if baselines != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = baselines.Discard(cleanupCtx, overlayPath)
	}
}

// EnsureWorkerBranch hands back the job's branch tree, rebuilding it first
// when retention reclaimed it.
func (b *BranchService) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error) {
	task, ok := b.store.getTask(ctx, strings.TrimSpace(jobID))
	if !ok || task == nil {
		return nil, nil, fmt.Errorf("%w: job %q not found", ErrWorkerBranchUnavailable, jobID)
	}
	b.mu.Lock()
	ws := b.workerWorkspace
	b.mu.Unlock()
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
func (b *BranchService) ListBranchJobs(ctx context.Context) ([]BranchJob, error) {
	rows, err := b.store.queries.ListWorkerJobsForBranchRetention(ctx)
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
			if task, ok := b.store.getTask(ctx, row.ID); ok && task != nil {
				branchRoot = task.WorkspaceRoot
			}
		}
		out = append(out, BranchJob{
			ID: row.ID, ProjectID: row.ProjectID, Branch: branchName(db.StringFromNull(row.WorkspaceRelpath)),
			Sealed: branchJobSealed(status, mergeStatus, overlayID, branchRoot),
		})
	}
	return out, nil
}

// branchName is the directory a branch root names, or "" for none.
func branchName(root string) string {
	if root = strings.TrimSpace(root); root == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(root))
}

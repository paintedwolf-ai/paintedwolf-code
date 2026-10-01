package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// ClaimWorkerBranch provisions a write-worker branch once.
func (q *InMemoryQueue) ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, ErrWorkerBranchClaimFailed
	}
	release, err := q.branchClaims.Acquire(ctx, jobID)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok || job == nil {
		q.mu.Unlock()
		return nil, ErrWorkerBranchClaimFailed
	}
	if !q.prerequisitesReady(job.task) {
		q.mu.Unlock()
		return nil, ErrWorkerPrerequisitesPending
	}
	task := job.task
	ws := q.workerWorkspace
	projects := q.projects
	q.mu.Unlock()

	if !task.EffectiveScope().IsWrite() {
		cp := task
		return &cp, nil
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		cp := task
		return &cp, nil
	}
	if ws == nil {
		return nil, ErrWorkerBranchClaimFailed
	}
	roots := TaskRootRefs(ctx, &task, projects)
	if len(roots) == 0 {
		return nil, ErrWorkerBranchClaimFailed
	}
	baseLease, err := q.holdBaseOverlay(ctx, &task)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	defer baseLease.Release()
	sources, err := workspaceSourceRoots(&task, roots, q)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	binding, layout, err := ws.CreateWorkerWorkspaceFromSources(ctx, roots, sources, strings.TrimSpace(task.WorkspaceRootID), task.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return nil, ErrWorkerBranchClaimFailed
	}
	baselines := transientBaselines(binding.Root)
	baselinePath, err := baselines.Capture(ctx, task.ID, workspacebaseline.Branch(layout.Roots, binding.Root))
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot workspace: %w", ErrWorkerBranchClaimFailed, err)
	}
	published := false
	defer func() {
		if !published {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = baselines.Discard(cleanupCtx, baselinePath)
		}
	}()
	lost := false
	found := false
	q.mu.Lock()
	if j, ok := q.jobs[jobID]; ok && j != nil {
		found = true
		if strings.TrimSpace(j.task.WorkspaceRoot) == "" {
			j.task.WorkspaceRoot = binding.Root
			j.task.WorkspaceBaselinePath = baselinePath
			published = true
		} else if j.task.WorkspaceRoot != binding.Root {
			lost = true
		}
		task = j.task
	}
	q.mu.Unlock()
	if !found {
		return nil, ErrWorkerBranchClaimFailed
	}
	if lost {
		_ = ws.DestroyWorkerWorkspace(binding)
	}
	cp := task
	return &cp, nil
}

// transientBaselines keeps baselines beside the branch tree.
func transientBaselines(branchRoot string) *workspacebaseline.Store {
	baselineRoot := filepath.Join(filepath.Dir(branchRoot), ".baselines")
	return workspacebaseline.New(nil, sourceblob.New(filepath.Join(baselineRoot, "source-content")), filepath.Join(baselineRoot, "worker-baselines"))
}

func (q *InMemoryQueue) captureOverlay(ctx context.Context, task *api.WorkerTask, projects ProjectStore) (string, error) {
	// A branch with no recorded topology is a single-root tree.
	roots := TaskRootRefs(ctx, task, projects)
	captured, err := transientBaselines(task.WorkspaceRoot).CaptureOverlay(ctx, task.ID, task.WorkspaceBaselinePath, roots, task.WorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("worker %s: capture overlay: %w", task.ID, err)
	}
	return captured.Path, nil
}

// EnsureWorkerBranch hands back the job's branch tree, rebuilding it first
// when retention reclaimed it.
func (q *InMemoryQueue) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error) {
	task, ok := q.Get(strings.TrimSpace(jobID))
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

func (q *InMemoryQueue) holdBaseOverlay(ctx context.Context, task *api.WorkerTask) (*BranchLease, error) {
	baseID := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseID == "" {
		return nil, nil
	}
	base, ok := q.Get(baseID)
	if !ok || base == nil || base.MergeStatus == api.WorkerMergeStatusMerged || strings.TrimSpace(base.WorkspaceRoot) == "" {
		return nil, nil
	}
	_, lease, err := q.EnsureWorkerBranch(ctx, baseID)
	if err != nil {
		return nil, fmt.Errorf("base overlay %q: %w", baseID, err)
	}
	return lease, nil
}

// destroyWorkspaceRoot removes an isolated sandbox copy after a terminal
// transition; a queue without a workspace manager leaves the path alone.
func (q *InMemoryQueue) destroyWorkspaceRoot(jobID, root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if ws == nil {
		return
	}
	_ = ws.DestroyWorkerWorkspace(&workspace.Binding{ID: jobID, Root: root})
}

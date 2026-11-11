package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/workerplacement"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// ClaimWorkerBranch provisions a write-worker branch once.
func (q *memoryQueueQuery) ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error) {
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

	if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) != "" {
		cp := task
		return &cp, nil
	}
	if ws == nil {
		return nil, ErrWorkerBranchClaimFailed
	}
	return q.materializeMemoryWorkerBranch(ctx, jobID, task, ws, projects)
}

func (q *memoryQueueQuery) materializeMemoryWorkerBranch(
	ctx context.Context,
	jobID string,
	task api.WorkerTask,
	ws WorkerWorkspaceManager,
	projects ProjectStore,
) (*api.WorkerTask, error) {
	roots := TaskRootRefs(ctx, &task, projects)
	if len(roots) == 0 {
		return nil, ErrWorkerBranchClaimFailed
	}
	parentCheckpoint, err := stackedParentCheckpoint(ctx, &task, roots, q.self, ws)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	binding, layout, err := createTaskBranch(ctx, ws, roots, strings.TrimSpace(task.WorkspaceRootID), task.ID, parentCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerBranchClaimFailed, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return nil, ErrWorkerBranchClaimFailed
	}
	baselines := transientBaselines(binding.Root)
	baselinePath := ""
	if !workspace.BranchUsesCheckpoints(binding.Root) {
		baselinePath, err = baselines.Capture(ctx, task.ID, workspacebaseline.Branch(layout.Roots, binding.Root))
		if err != nil {
			return nil, fmt.Errorf("%w: snapshot workspace: %w", ErrWorkerBranchClaimFailed, err)
		}
	}
	return q.publishMemoryWorkerBranch(ctx, jobID, binding, baselines, baselinePath)
}

func (q *memoryQueueQuery) publishMemoryWorkerBranch(
	ctx context.Context,
	jobID string,
	binding *workspace.Binding,
	baselines *workspacebaseline.Store,
	baselinePath string,
) (*api.WorkerTask, error) {
	published := false
	defer func() {
		if !published {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = baselines.Discard(cleanupCtx, baselinePath)
		}
	}()
	var task api.WorkerTask
	var ws WorkerWorkspaceManager
	lost, found := false, false
	q.mu.Lock()
	ws = q.workerWorkspace
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
	if lost && ws != nil {
		_ = ws.DestroyWorkerWorkspace(ctx, binding)
	}
	cp := task
	return &cp, nil
}

// transientBaselines keeps baselines beside the branch tree.
func transientBaselines(branchRoot string) *workspacebaseline.Store {
	baselineRoot := filepath.Join(filepath.Dir(branchRoot), ".baselines")
	return workspacebaseline.New(nil, sourceblob.New(filepath.Join(baselineRoot, "source-content")), filepath.Join(baselineRoot, "worker-baselines"))
}

func captureOverlay(ctx context.Context, task *api.WorkerTask, projects ProjectStore) (string, error) {
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
func (q *memoryQueueCore) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error) {
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

// destroyWorkspaceRoot removes an isolated sandbox copy after a terminal
// transition; a queue without a workspace manager leaves the path alone.
func destroyWorkspaceRoot(ctx context.Context, q *InMemoryQueue, jobID, root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if ws == nil {
		return
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	defer cancel()
	_ = ws.DestroyWorkerWorkspace(cleanupCtx, &workspace.Binding{ID: jobID, Root: root})
}

func workerWorkspaceIdentity(ctx context.Context, database db.DBTX, root, baseline string) (string, string, error) {
	dataDir, err := db.Directory(ctx, database)
	if err != nil {
		return "", "", err
	}
	// A branch captured by checkpoint records no baseline.
	id := ""
	if strings.TrimSpace(baseline) != "" {
		id, err = workspacebaseline.ID(baseline)
		if err != nil {
			return "", "", err
		}
		resolved, err := workspacebaseline.Path(dataDir, id)
		if err != nil {
			return "", "", err
		}
		if filepath.Clean(baseline) != resolved {
			return "", "", fmt.Errorf("worker baseline is outside the active installation")
		}
	}
	rel, ok, err := workerplacement.LogicalFromPhysical(dataDir, root)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("worker branch is outside the active installation")
	}
	return id, rel, nil
}

// TaskRootRefs returns a worker's captured or attached roots.
func TaskRootRefs(ctx context.Context, task *api.WorkerTask, projects ProjectStore) []projectroot.RootRef {
	if task == nil {
		return nil
	}
	if branchRoot := strings.TrimSpace(task.WorkspaceRoot); branchRoot != "" {
		// The recorded topology outlives the tree, which retention may reclaim.
		roots, err := workspace.LoadBranchRoots(branchRoot)
		if err != nil {
			return nil
		}
		return roots
	}
	if projects != nil && strings.TrimSpace(task.ProjectID) != "" {
		if p, err := projects.Get(ctx, task.ProjectID); err == nil && p != nil {
			if refs := project.RootRefsFrom(p); len(refs) > 0 {
				return refs
			}
		}
	}
	path := strings.TrimSpace(task.WorkspacePath)
	rootID := strings.TrimSpace(task.WorkspaceRootID)
	if path == "" || rootID == "" {
		return nil
	}
	return []projectroot.RootRef{{
		ID:        rootID,
		Path:      path,
		IsPrimary: true,
	}}
}

// ReconcileProjectSandboxes removes sandboxes absent from current task state. Each candidate
// settles first; a candidate that cannot keeps its data and its retry state. Live workers are
// retained, so this runs safely while the engine serves them.
func ReconcileProjectSandboxes(
	ctx context.Context,
	sandboxRoot, primaryDir string,
	settle workspace.SandboxSettler,
	loadTasks func(context.Context) ([]api.WorkerTask, error),
) (removed int, err error) {
	return workspace.ReconcileStaleSandboxes(ctx, sandboxRoot, primaryDir, settle, func(ctx context.Context) (map[string]struct{}, error) {
		tasks, loadErr := loadTasks(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		retained := ActiveSandboxJobIDs(tasks)
		for _, task := range tasks {
			if task.EffectiveScope().IsWrite() && task.WorkspaceOverlayPath != "" && task.MergeStatus == api.WorkerMergeStatusPending {
				retained[task.ID] = struct{}{}
			}
		}
		return retained, nil
	})
}

// ActiveSandboxJobIDs returns the worker job IDs whose on-disk sandboxes
// project-open reconciliation keeps.
func ActiveSandboxJobIDs(tasks []api.WorkerTask) map[string]struct{} {
	out := make(map[string]struct{})
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() {
			continue
		}
		switch task.MergeStatus {
		case api.WorkerMergeStatusMerged,
			api.WorkerMergeStatusOrphaned,
			api.WorkerMergeStatusRejected,
			api.WorkerMergeStatusAborted:
			continue
		case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
		}
		switch task.Status {
		case api.WorkerStatusFailed, api.WorkerStatusCanceled:
			continue
		case api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusComplete, api.WorkerStatusHeld:
		}
		out[task.ID] = struct{}{}
	}
	return out
}

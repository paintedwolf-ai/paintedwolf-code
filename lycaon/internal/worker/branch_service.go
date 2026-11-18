package worker

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// BranchService prepares, retains, and reconstructs worker branch state.
type BranchService struct {
	store           *SQLStore
	changes         *jobChanges
	mu              sync.Mutex
	branchClaims    keylock.Group
	preparations    map[string]*api.WorkspacePreparation
	workerWorkspace WorkerWorkspaceManager
	projects        ProjectStore
	baselines       *workspacebaseline.Store
	lanes           *WorkerLanes
}

func (b *BranchService) decorateTask(task *api.WorkerTask) {
	b.mu.Lock()
	defer b.mu.Unlock()
	task.WorkspacePreparation = b.preparations[task.ID]
}

func (b *BranchService) rootRefs(ctx context.Context, task *api.WorkerTask) []projectroot.RootRef {
	b.mu.Lock()
	projects := b.projects
	b.mu.Unlock()
	return TaskRootRefs(ctx, task, projects)
}

// SetWorkerWorkspaceManager provisions write-worker branches on first use.
func (q *BranchService) SetWorkerWorkspaceManager(ws WorkerWorkspaceManager) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.workerWorkspace = ws
}

// SetWorkerLanes runs worker rounds' write jobs in their workers' lanes.
func (q *BranchService) SetWorkerLanes(lanes *WorkerLanes) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.lanes = lanes
}

// SetProjectStore loads project roots when provisioning worker sandboxes.
func (q *BranchService) SetProjectStore(store ProjectStore) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.projects = store
}

func (q *BranchService) SetBaselineStore(store *workspacebaseline.Store) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.baselines = store
}

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
func (b *BranchService) SetWorkerWorkspaceManager(ws WorkerWorkspaceManager) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.workerWorkspace = ws
}

// SetWorkerLanes runs worker rounds' write jobs in their workers' lanes.
func (b *BranchService) SetWorkerLanes(lanes *WorkerLanes) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lanes = lanes
}

// SetProjectStore loads project roots when provisioning worker sandboxes.
func (b *BranchService) SetProjectStore(store ProjectStore) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projects = store
}

func (b *BranchService) SetBaselineStore(store *workspacebaseline.Store) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baselines = store
}

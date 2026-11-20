package wiring

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil/workertopologytest"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type mockWorkspaceBinder struct {
	mu sync.Mutex
}

func (b *mockWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, primaryDir, legID string) (*workspace.Binding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	root := filepath.Join(primaryDir, settingsoverlay.DirName(), "mock-wt", legID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &workspace.Binding{ID: legID, Root: root}, nil
}

func (b *mockWorkspaceBinder) DestroyWorkerWorkspace(context.Context, *workspace.Binding) error {
	return nil
}

type autoCompleteTopologyWorkers struct {
	orchestration.TopologyWorkers
	queue worker.WorkerQueue
	mu    sync.Mutex
}

func (a *autoCompleteTopologyWorkers) DispatchUnit(ctx context.Context, batchID, unitID string) (*orchestration.TopologyUnit, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	unit, err := a.GetUnit(ctx, batchID, unitID)
	if err != nil {
		return nil, err
	}
	return workertopologytest.DispatchAndSettle(ctx, a.TopologyWorkers, a.queue, batchID, unitID, wire.WorkerResult{
		Status:  "complete",
		Summary: unit.WorkID + " done",
	})
}

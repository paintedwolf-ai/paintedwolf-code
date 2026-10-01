package wiring

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
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

func (b *mockWorkspaceBinder) DestroyWorkerWorkspace(*workspace.Binding) error {
	return nil
}

type autoCompleteDelegation struct {
	inner delegation.DelegationManager
	store orchestration.PipelineDelegationStore
}

func (a *autoCompleteDelegation) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*wire.Leg, error) {
	leg, err := a.inner.DispatchLeg(ctx, delegationID, legID, sourceToolCallID)
	if err != nil {
		return nil, err
	}
	if err := a.inner.RecordOutcome(ctx, delegationID, legID, leg.WorkerID, wire.WorkerResult{
		Status:  "complete",
		Summary: leg.Title + " done",
	}); err != nil {
		return nil, err
	}
	return a.store.GetLeg(ctx, delegationID, legID)
}

func (a *autoCompleteDelegation) Abort(ctx context.Context, delegationID, reason string) error {
	return a.inner.Abort(ctx, delegationID, reason)
}

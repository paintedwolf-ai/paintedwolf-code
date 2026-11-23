package adaptertest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func (a *Mounted) find(viewID, operation string) (*mountedView, error) {
	a.mu.RLock()
	state := a.views[viewID]
	a.mu.RUnlock()
	if state == nil {
		return nil, a.failure(operation, adapter.ReasonViewNotFound, fmt.Errorf("fixture view %s is absent", viewID))
	}
	return state, nil
}

func (a *Mounted) IsViewActive(viewID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.views[viewID] != nil
}

func (a *Mounted) CommandAccess(viewID string) (adapter.CommandAccessSpec, error) {
	state, err := a.find(viewID, "command_access")
	if err != nil {
		return adapter.CommandAccessSpec{}, err
	}
	roots := make(map[string]string, len(state.view.Roots))
	for _, root := range state.view.Roots {
		roots[root.RootID] = root.WorkspacePath
	}
	return adapter.CommandAccessSpec{ViewID: viewID, AdapterID: a.ID(), PrimaryRootID: state.view.PrimaryRootID,
		WorkDir: state.view.PrimaryWorkPath, RootPaths: roots, HiddenBackingDir: state.view.BackingDir,
		DenyDirectSource: append([]string(nil), state.deny...), Facts: a.facts, AccessCount: state.access.Add(1)}, nil
}

func (a *Mounted) CheckpointBarrier(ctx context.Context, viewID string) (adapter.BarrierReceipt, func() error, error) {
	state, err := a.find(viewID, "checkpoint_barrier")
	if err != nil {
		return adapter.BarrierReceipt{}, nil, err
	}
	fail := func(err error) (adapter.BarrierReceipt, func() error, error) {
		return adapter.BarrierReceipt{}, nil, a.failure("checkpoint_barrier", adapter.ReasonBarrierFailed, err)
	}
	if state.quiesce != nil {
		if err := state.quiesce(ctx, viewID); err != nil {
			return fail(err)
		}
	}
	token, err := state.view.Core.BeginCheckpointBarrier(ctx)
	if err != nil {
		return fail(err)
	}
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = state.view.Core.EndCheckpointBarrier(token)
			if errors.Is(releaseErr, layeredfs.ErrBarrierExpired) {
				releaseErr = a.failure("checkpoint_barrier", adapter.ReasonBarrierExpired, releaseErr)
			}
		})
		return releaseErr
	}
	if state.flush != nil {
		if err := state.flush(ctx, viewID); err != nil {
			_ = release()
			return fail(err)
		}
	}
	return adapter.BarrierReceipt{ViewID: viewID, AdapterID: a.ID(), Token: token, WritersQuiesced: true,
		OperationsDrained: true, DirtyDataFlushed: true, WritableMappingsFlushed: true}, release, nil
}

func (a *Mounted) Release(ctx context.Context, viewID string) (adapter.ReleaseReceipt, error) {
	receipt := adapter.ReleaseReceipt{ViewID: viewID, AdapterID: a.ID()}
	state, err := a.find(viewID, "release")
	if err != nil {
		receipt.ReasonCode = adapter.ReasonViewNotFound
		return receipt, err
	}
	fail := func(err error) (adapter.ReleaseReceipt, error) {
		receipt.ReasonCode, receipt.Diagnostic = adapter.ReasonCleanupFailed, err.Error()
		return receipt, a.failure("release", adapter.ReasonCleanupFailed, err)
	}
	if err := state.view.Core.ReleaseView(); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if a.cfg.UnmountViewFn != nil {
		if err := a.cfg.UnmountViewFn(ctx, viewID, state.view.RuntimeViewDir); err != nil {
			return fail(err)
		}
	}
	if err := os.RemoveAll(state.view.RuntimeViewDir); err != nil {
		return fail(err)
	}
	a.mu.Lock()
	delete(a.views, viewID)
	a.mu.Unlock()
	receipt.Settled, receipt.CleanedPaths = true, []string{state.view.RuntimeViewDir}
	return receipt, nil
}

package fuselinux

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// NewBarrierProvider returns checkpoint.BarrierCallbacks wired to a.CheckpointBarrier(ctx, viewID)
// so Publisher.Publish holds the adapter and FUSE server mutation gate across the entire capture.
func (a *Adapter) NewBarrierProvider(viewID string) checkpoint.BarrierCallbacks {
	return checkpoint.BarrierCallbacks{
		AcquireAdapterBarrier: func(ctx context.Context) (layeredfs.BarrierToken, func() error, error) {
			rec, release, err := a.CheckpointBarrier(ctx, viewID)
			if err != nil {
				return layeredfs.BarrierToken{}, nil, err
			}
			return rec.Token, release, nil
		},
	}
}

// SetBarrierHooks configures the QuiesceHook and FlushClientHook for viewID.
func (a *Adapter) SetBarrierHooks(
	viewID string,
	quiesceHook func(ctx context.Context, viewID string) error,
	flushClientHook func(ctx context.Context, viewID string) error,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.views[viewID]
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "set_barrier_hooks", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	st.quiesceHook = quiesceHook
	st.flushHook = flushClientHook
	return nil
}

// MountedViewBindingForRoot constructs a live privategit.MountedViewBinding for (viewID, rootID).
func (a *Adapter) MountedViewBindingForRoot(viewID, workerID, rootID string) (privategit.MountedViewBinding, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return privategit.MountedViewBinding{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "mounted_binding", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if rootID == "" {
		rootID = st.primaryRoot
	}
	if workerID == "" {
		workerID = viewID
	}
	rm := st.mounts[rootID]
	if rm == nil {
		return privategit.MountedViewBinding{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "mounted_binding", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "missing mount for root " + rootID}
	}
	return privategit.MountedViewBinding{
		ViewID: viewID, WorkerID: workerID, RootID: rootID,
		TrunkDir: rm.grant.SourcePath, MountPath: rm.mountPath,
		AdapterID: string(adapter.AdapterFUSELinux), Core: st.core,
		MountID: rm.facts.MountID, ConnectionID: rm.facts.ConnectionID,
		Active:   a.IsViewActive(viewID),
		IsActive: func() bool { return a.IsViewActive(viewID) },
	}, nil
}

// BindPrivateGit registers or updates a live MountedViewBinding in mgr for viewID so
// privategit operations are bound to this mounted FUSE view and automatically revoked if the
// FUSE server dies or Release is called.
func (a *Adapter) BindPrivateGit(
	viewID string,
	workerID string,
	mgr *privategit.Manager,
	authorizedGitDirs ...string,
) error {
	if mgr == nil {
		return fmt.Errorf("fuselinux: nil privategit.Manager")
	}
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok && st != nil {
		st.privateGitMgr = mgr
	}
	a.mu.Unlock()
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "bind_privategit", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	binding, err := a.MountedViewBindingForRoot(viewID, workerID, st.primaryRoot)
	if err != nil {
		return err
	}
	_ = mgr.ReleaseView(viewID)
	if err := mgr.RegisterMountedViewBinding(binding); err != nil {
		return err
	}
	return mgr.MountedClaimWithBinding(binding, authorizedGitDirs...)
}

// IsViewActive reports whether viewID is currently mounted, not releasing, and its FUSE server is healthy.
func (a *Adapter) IsViewActive(viewID string) bool {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil || st.releasing.Load() {
		return false
	}
	for _, m := range st.mounts {
		if m.unmounted.Load() || checkServerAlive(m) != nil || !isMountOwnedAndActive(m, true) {
			return false
		}
	}
	return true
}

// CommandAccess returns the host execution paths and source-denial metadata for viewID without remounting.
func (a *Adapter) CommandAccess(viewID string) (adapter.CommandAccessSpec, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "command_access", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if st.releasing.Load() {
		return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "command_access", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "view " + viewID + " is releasing"}
	}
	for _, m := range st.mounts {
		if err := checkServerAlive(m); err != nil {
			return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "command_access", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
		}
		if !isMountOwnedAndActive(m, true) {
			return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "command_access", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "mount inactive on " + m.mountPath}
		}
	}
	count := st.accessCount.Add(1)
	rootPaths := make(map[string]string, len(st.view.Roots))
	for _, r := range st.view.Roots {
		rootPaths[r.RootID] = r.WorkspacePath
	}
	return adapter.CommandAccessSpec{
		ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, PrimaryRootID: st.view.PrimaryRootID,
		WorkDir: st.view.PrimaryWorkPath, RootPaths: rootPaths, HiddenBackingDir: st.view.BackingDir,
		DenyDirectSource: append([]string(nil), st.denySourcePaths...), Facts: a.facts, AccessCount: count,
	}, nil
}

// FlushClientData verifies that all mounted roots and FUSE servers for viewID are alive,
// runs FlushClientHook to flush writable mappings/descriptors, syncs all mounted files and directories,
// and flushes server state to Core.
func (a *Adapter) FlushClientData(ctx context.Context, viewID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil || st.releasing.Load() {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "flush_client", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if st.flushHook == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "FlushClientHook is required to flush writable client mappings and descriptors"}
	}
	if err := st.flushHook(ctx, viewID); err != nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "flush_client", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
	}
	var totalFiles, totalDirs int
	for rootID, m := range st.mounts {
		if err := verifyMountAndServerForBarrier(st, viewID, rootID, m, "flush_client"); err != nil {
			return err
		}
		// An active barrier already flushed this root with writers quiesced. Flushing through the
		// mount again could only wait on that barrier's own mutation gate.
		if !m.server.IsBarrierActive() {
			fSync, dSync, err := a.syncMountedRootFiles(ctx, st, rootID, m)
			if err != nil {
				return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "flush_client", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
			}
			totalFiles += fSync
			totalDirs += dSync
		}
		if err := m.server.FlushForCheckpoint(ctx, viewID); err != nil {
			return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "flush_client", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
		}
	}
	st.dirtyMu.Lock()
	st.lastFilesSynced, st.lastDirsSynced = totalFiles, totalDirs
	st.dirtyMu.Unlock()
	return nil
}

// CheckpointBarrier coordinates writer quiescence, client file/mapping flush, FUSE server request
// drain + mutation gating, and final Core.BeginCheckpointBarrier freeze in strict order.
func (a *Adapter) CheckpointBarrier(ctx context.Context, viewID string) (adapter.BarrierReceipt, func() error, error) {
	receipt := adapter.BarrierReceipt{ViewID: viewID, AdapterID: adapter.AdapterFUSELinux}
	if err := ctx.Err(); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil || st.releasing.Load() {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		if err := verifyMountAndServerForBarrier(st, viewID, rootID, m, "checkpoint_barrier"); err != nil {
			return receipt, nil, err
		}
	}
	if st.quiesceHook == nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "checkpoint barrier requires QuiesceHook to establish WritersQuiesced"}
	}
	if err := st.quiesceHook(ctx, viewID); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
	}
	receipt.WritersQuiesced = true
	if st.flushHook == nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "checkpoint barrier requires FlushClientHook to establish WritableMappingsFlushed"}
	}
	if err := st.flushHook(ctx, viewID); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
	}
	receipt.WritableMappingsFlushed = true

	var gateReleases []func()
	releaseGates := func() {
		for i := len(gateReleases) - 1; i >= 0; i-- {
			gateReleases[i]()
		}
	}
	var totalFiles, totalDirs int
	for rootID, m := range st.mounts {
		fSync, dSync, err := a.syncMountedRootFiles(ctx, st, rootID, m)
		if err != nil {
			releaseGates()
			return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
		}
		totalFiles += fSync
		totalDirs += dSync
		relGate, gErr := m.server.BeginBarrierDrain(ctx, viewID)
		if gErr != nil {
			releaseGates()
			return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: classifyBarrierError(gErr), Diagnostic: gErr.Error(), Cause: gErr}
		}
		gateReleases = append(gateReleases, relGate)
	}
	st.dirtyMu.Lock()
	st.lastFilesSynced, st.lastDirsSynced = totalFiles, totalDirs
	st.dirtyMu.Unlock()
	receipt.OperationsDrained = true
	receipt.DirtyDataFlushed = true

	token, err := st.core.BeginCheckpointBarrier(ctx)
	if err != nil {
		releaseGates()
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "checkpoint_barrier", ReasonCode: classifyBarrierError(err), Diagnostic: err.Error(), Cause: err}
	}
	receipt.Token = token
	var released atomic.Bool
	var releaseErr error
	releaseFn := func() error {
		if released.CompareAndSwap(false, true) {
			releaseErr = st.core.EndCheckpointBarrier(token)
			releaseGates()
		}
		return releaseErr
	}
	return receipt, releaseFn, nil
}

// Release revokes private-git bindings, unmounts each root while retaining view state on failure,
// closes FUSE servers only after unmount succeeds, releases Core, and removes RuntimeViewDir.
func (a *Adapter) Release(ctx context.Context, viewID string) (adapter.ReleaseReceipt, error) {
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok && st != nil {
		st.releasing.Store(true)
	}
	a.mu.Unlock()
	if !ok || st == nil {
		return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, Settled: false, ReasonCode: adapter.ReasonViewNotFound},
			&adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "release", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		if !m.unmounted.Load() {
			alive := checkServerAlive(m) == nil
			if uErr := a.unmountOwnedRoot(ctx, m, alive); uErr != nil {
				return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: uErr.Error()}, uErr
			}
			m.unmounted.Store(true)
		}
		if !m.serverClosed.Load() {
			if m.server != nil {
				_ = m.server.Close()
			}
			m.serverClosed.Store(true)
		}
	}
	if !st.coreReleased.Load() {
		if cErr := st.core.ReleaseView(); cErr != nil && !errors.Is(cErr, layeredfs.ErrViewReleased) {
			aErr := &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "release", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: cErr.Error(), Cause: cErr}
			return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: aErr.Error()}, aErr
		}
		st.coreReleased.Store(true)
	}
	if !st.runtimeCleaned.Load() {
		if rmErr := a.removeRuntimeDir(st.view.RuntimeViewDir); rmErr != nil {
			aErr := &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "release", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: fmt.Sprintf("remove runtime dir %s: %v", st.view.RuntimeViewDir, rmErr), Cause: rmErr}
			return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: aErr.Error()}, aErr
		}
		st.runtimeCleaned.Store(true)
	}
	a.mu.Lock()
	delete(a.views, viewID)
	a.mu.Unlock()
	return adapter.ReleaseReceipt{
		ViewID: viewID, AdapterID: adapter.AdapterFUSELinux, Settled: true,
		ReasonCode: adapter.ReasonNone, CleanedPaths: []string{st.view.RuntimeViewDir},
	}, nil
}

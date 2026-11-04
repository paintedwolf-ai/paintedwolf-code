package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// NewBarrierProvider returns checkpoint.BarrierCallbacks that acquire and hold the adapter's
// CheckpointBarrier (including the server's mutating RPC gate and Core barrier) across publication.
func (a *Adapter) NewBarrierProvider(viewID string) checkpoint.BarrierCallbacks {
	return checkpoint.BarrierCallbacks{
		AcquireAdapterBarrier: func(ctx context.Context) (layeredfs.BarrierToken, func() error, error) {
			rec, release, err := a.CheckpointBarrier(ctx, viewID)
			return rec.Token, release, err
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
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "set_barrier_hooks", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	st.quiesceHook = quiesceHook
	st.flushHook = flushClientHook
	return nil
}

// BindPrivateGit registers or updates a live MountedViewBinding in mgr for viewID so
// privategit operations are bound to this mounted view and automatically revoked if the
// NFS server dies or Release is called.
func (a *Adapter) BindPrivateGit(
	viewID string,
	workerID string,
	mgr *privategit.Manager,
	authorizedGitDirs ...string,
) error {
	if mgr == nil {
		return fmt.Errorf("nfsdarwin: nil privategit.Manager")
	}
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok && st != nil {
		st.privateGitMgr = mgr
	}
	a.mu.Unlock()
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "bind_privategit", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if workerID == "" {
		workerID = viewID
	}
	pm := st.mounts[st.primaryRoot]
	if pm == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "bind_privategit", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "missing primary mount"}
	}
	bindingFor := func(rootID string, mount *rootMount) privategit.MountedViewBinding {
		return privategit.MountedViewBinding{
			ViewID: viewID, WorkerID: workerID, RootID: rootID,
			TrunkDir: mount.grant.SourcePath, MountPath: mount.mountPath,
			AdapterID: string(adapter.AdapterNFSDarwin), Core: st.core,
			MountID: mount.facts.MountID, ConnectionID: mount.facts.ConnectionID,
			Active:   a.IsViewActive(viewID),
			IsActive: func() bool { return a.IsViewActive(viewID) },
		}
	}
	binding := bindingFor(st.primaryRoot, pm)
	if err := mgr.ReleaseView(viewID); err != nil {
		return err
	}
	if err := mgr.RegisterMountedViewBinding(binding); err != nil {
		return err
	}
	if err := mgr.MountedClaimWithBinding(binding, authorizedGitDirs...); err != nil {
		return err
	}
	for rootID, mount := range st.mounts {
		if rootID == st.primaryRoot {
			continue
		}
		if err := mgr.BindSecondaryRoot(bindingFor(rootID, mount), authorizedGitDirs...); err != nil {
			return err
		}
	}
	return nil
}

// IsViewActive reports whether viewID is currently mounted, not releasing, and its loopback NFS server is healthy.
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
		return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "command_access", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if st.releasing.Load() {
		return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "command_access", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "view " + viewID + " is releasing"}
	}
	for _, m := range st.mounts {
		if err := checkServerAlive(m); err != nil {
			return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "command_access", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
		}
		if !isMountOwnedAndActive(m, true) {
			return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "command_access", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "mount inactive on " + m.mountPath}
		}
	}
	count := st.accessCount.Add(1)
	rootPaths := make(map[string]string, len(st.view.Roots))
	for _, r := range st.view.Roots {
		rootPaths[r.RootID] = r.WorkspacePath
	}
	return adapter.CommandAccessSpec{
		ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, PrimaryRootID: st.view.PrimaryRootID,
		WorkDir: st.view.PrimaryWorkPath, RootPaths: rootPaths, HiddenBackingDir: st.view.BackingDir,
		DenyDirectSource: append([]string(nil), st.denySourcePaths...), Facts: a.facts, AccessCount: count,
	}, nil
}

// FlushClientData verifies that all mounted roots and loopback NFS servers for viewID are alive,
// runs FlushClientHook to flush writable mappings/descriptors, syncs all mounted files and directories,
// and flushes pending NFSv3 UNSTABLE writes to Core.
func (a *Adapter) FlushClientData(ctx context.Context, viewID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil || st.releasing.Load() {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if st.flushHook == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "FlushClientHook is required to flush writable client mappings and descriptors"}
	}
	if err := st.flushHook(ctx, viewID); err != nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
	}
	st.resetFlushStats()
	for rootID, m := range st.mounts {
		if err := verifyMountAndServerForBarrier(st, viewID, rootID, m, "flush_client"); err != nil {
			return err
		}
		if err := a.syncMountedRootFiles(ctx, st, rootID, m); err != nil {
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
		}
		if err := m.server.FlushForCheckpoint(ctx, viewID); err != nil {
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
		}
	}
	return nil
}

// CheckpointBarrier coordinates writer quiescence, client file/mapping flush, server UNSTABLE write
// drain + mutation gating, and final Core.BeginCheckpointBarrier freeze in strict order.
func (a *Adapter) CheckpointBarrier(ctx context.Context, viewID string) (adapter.BarrierReceipt, func() error, error) {
	receipt := adapter.BarrierReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin}
	if err := ctx.Err(); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil || st.releasing.Load() {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		if err := verifyMountAndServerForBarrier(st, viewID, rootID, m, "checkpoint_barrier"); err != nil {
			return receipt, nil, err
		}
	}
	if st.quiesceHook == nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "checkpoint barrier requires QuiesceHook to establish WritersQuiesced"}
	}
	if err := st.quiesceHook(ctx, viewID); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
	}
	receipt.WritersQuiesced = true
	if st.flushHook == nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "checkpoint barrier requires FlushClientHook to establish WritableMappingsFlushed"}
	}
	if err := st.flushHook(ctx, viewID); err != nil {
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
	}
	receipt.WritableMappingsFlushed = true

	var gateReleases []func()
	releaseGates := func() {
		for i := len(gateReleases) - 1; i >= 0; i-- {
			gateReleases[i]()
		}
	}
	st.resetFlushStats()
	for rootID, m := range st.mounts {
		if err := a.syncMountedRootFiles(ctx, st, rootID, m); err != nil {
			releaseGates()
			return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
		}
		relGate, gErr := m.server.BeginBarrierDrain(ctx, viewID)
		if gErr != nil {
			releaseGates()
			return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: classifyBarrierReason(gErr), Diagnostic: gErr.Error(), Cause: gErr}
		}
		gateReleases = append(gateReleases, relGate)
	}
	receipt.OperationsDrained = true
	receipt.DirtyDataFlushed = true

	token, err := st.core.BeginCheckpointBarrier(ctx)
	if err != nil {
		releaseGates()
		return receipt, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: classifyBarrierReason(err), Diagnostic: err.Error(), Cause: err}
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

// KillServerForView forcibly closes the loopback NFSv3 server for viewID to simulate a mid-session server crash.
func (a *Adapter) KillServerForView(viewID string) error {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "kill_server", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		m.serverDead.Store(true)
		if m.server != nil {
			_ = m.server.Close()
		}
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
	}
	return nil
}

// Release revokes private-git bindings, unmounts each root while retaining view state on failure,
// closes loopback NFSv3 servers only after unmount succeeds, releases Core, and removes RuntimeViewDir.
func (a *Adapter) Release(ctx context.Context, viewID string) (adapter.ReleaseReceipt, error) {
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok && st != nil {
		st.releasing.Store(true)
	}
	a.mu.Unlock()
	if !ok || st == nil {
		return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonViewNotFound},
			&adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	st.releaseMu.Lock()
	defer st.releaseMu.Unlock()
	var flushDiagnostics []string
	for rootID, m := range st.mounts {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		if !m.unmounted.Load() {
			alive := checkServerAlive(m) == nil
			if uErr := a.unmountOwnedRoot(ctx, m, alive); uErr != nil {
				return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: uErr.Error()}, uErr
			}
			m.unmounted.Store(true)
		}
		if !m.serverClosed.Load() {
			if m.server != nil {
				// Bytes the client was told are held exist only here once the mount is gone. A failure
				// is reported rather than blocking release: retrying cannot make the disk accept them.
				if fErr := m.server.FlushForCheckpoint(context.WithoutCancel(ctx), viewID); fErr != nil {
					flushDiagnostics = append(flushDiagnostics, fmt.Sprintf("root %s lost unstable writes: %v", rootID, fErr))
				}
				m.server.ReleaseExport(viewID)
				_ = m.server.Close()
			}
			m.serverClosed.Store(true)
		}
	}
	if !st.coreReleased.Load() {
		if cErr := st.core.ReleaseView(); cErr != nil && !errors.Is(cErr, layeredfs.ErrViewReleased) {
			aErr := &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: cErr.Error(), Cause: cErr}
			return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: aErr.Error()}, aErr
		}
		st.coreReleased.Store(true)
	}
	if !st.runtimeCleaned.Load() {
		if rmErr := a.removeRuntimeDir(st.view.RuntimeViewDir); rmErr != nil {
			aErr := &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: fmt.Sprintf("remove runtime dir %s: %v", st.view.RuntimeViewDir, rmErr), Cause: rmErr}
			return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: aErr.Error()}, aErr
		}
		st.runtimeCleaned.Store(true)
	}
	a.mu.Lock()
	delete(a.views, viewID)
	a.mu.Unlock()
	return adapter.ReleaseReceipt{
		ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: true,
		ReasonCode: adapter.ReasonNone, CleanedPaths: []string{st.view.RuntimeViewDir},
		Diagnostic: strings.Join(flushDiagnostics, "; "),
	}, nil
}

package nfsdarwin

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

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
	if ok {
		st.privateGitMgr = mgr
	}
	a.mu.Unlock()
	if !ok {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "bind_privategit", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	if workerID == "" {
		workerID = viewID
	}
	pm := st.mounts[st.primaryRoot]
	if pm == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "bind_privategit", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "missing primary mount"}
	}
	binding := privategit.MountedViewBinding{
		ViewID:       viewID,
		WorkerID:     workerID,
		RootID:       st.primaryRoot,
		TrunkDir:     pm.grant.SourcePath,
		MountPath:    pm.mountPath,
		AdapterID:    string(adapter.AdapterNFSDarwin),
		Core:         st.core,
		MountID:      pm.facts.MountID,
		ConnectionID: pm.facts.ConnectionID,
		Active:       a.IsViewActive(viewID),
		IsActive: func() bool {
			return a.IsViewActive(viewID)
		},
	}
	_ = mgr.ReleaseView(viewID)
	if err := mgr.RegisterMountedViewBinding(binding); err != nil {
		return err
	}
	return mgr.MountedClaimWithBinding(binding, authorizedGitDirs...)
}

// IsViewActive reports whether viewID is currently mounted and its loopback NFS server is healthy.
func (a *Adapter) IsViewActive(viewID string) bool {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return false
	}
	for _, m := range st.mounts {
		if checkServerAlive(m) != nil {
			return false
		}
		if !isMountOwnedAndActive(m, true) {
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
	if !ok {
		return adapter.CommandAccessSpec{}, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "command_access", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
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
		ViewID:           viewID,
		AdapterID:        adapter.AdapterNFSDarwin,
		PrimaryRootID:    st.view.PrimaryRootID,
		WorkDir:          st.view.PrimaryWorkPath,
		RootPaths:        rootPaths,
		HiddenBackingDir: st.view.BackingDir,
		DenyDirectSource: append([]string(nil), st.denySourcePaths...),
		Facts:            a.facts,
		AccessCount:      count,
	}, nil
}

// FlushClientData verifies that all mounted roots and loopback NFS servers for viewID are alive,
// syncs the client mount directories, and flushes pending NFSv3 UNSTABLE writes to Core.
func (a *Adapter) FlushClientData(ctx context.Context, viewID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		if err := checkServerAlive(m); err != nil {
			if st.privateGitMgr != nil {
				st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
			}
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
		}
		if !isMountOwnedAndActive(m, true) {
			if st.privateGitMgr != nil {
				st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
			}
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: "mount ownership lost on " + m.mountPath}
		}
		if dirFile, oErr := os.Open(m.mountPath); oErr == nil {
			_ = dirFile.Sync()
			_ = dirFile.Close()
		}
		if err := m.server.FlushForCheckpoint(ctx, viewID); err != nil {
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
		}
	}
	if st.flushHook != nil {
		if err := st.flushHook(ctx, viewID); err != nil {
			return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "flush_client", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
		}
	}
	return nil
}

// CheckpointBarrier verifies mount/server health, runs the quiesce hook, acquires the
// layeredfs.Core checkpoint barrier, and flushes client dirty data and server UNSTABLE buffers.
func (a *Adapter) CheckpointBarrier(ctx context.Context, viewID string) (adapter.BarrierReceipt, func() error, error) {
	if err := ctx.Err(); err != nil {
		return adapter.BarrierReceipt{}, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return adapter.BarrierReceipt{}, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		if err := checkServerAlive(m); err != nil {
			if st.privateGitMgr != nil {
				st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
			}
			return adapter.BarrierReceipt{}, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
		}
	}
	if st.quiesceHook != nil {
		if err := st.quiesceHook(ctx, viewID); err != nil {
			return adapter.BarrierReceipt{}, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
		}
	}
	token, err := st.core.BeginCheckpointBarrier(ctx)
	if err != nil {
		return adapter.BarrierReceipt{}, nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "checkpoint_barrier", ReasonCode: adapter.ReasonBarrierFailed, Diagnostic: err.Error(), Cause: err}
	}
	var released atomic.Bool
	var releaseErr error
	releaseFn := func() error {
		if released.CompareAndSwap(false, true) {
			releaseErr = st.core.EndCheckpointBarrier(token)
		}
		return releaseErr
	}
	if fErr := a.FlushClientData(ctx, viewID); fErr != nil {
		_ = releaseFn()
		return adapter.BarrierReceipt{}, nil, fErr
	}
	return adapter.BarrierReceipt{
		ViewID:                  viewID,
		AdapterID:               adapter.AdapterNFSDarwin,
		Token:                   token,
		WritersQuiesced:         true,
		OperationsDrained:       true,
		DirtyDataFlushed:        true,
		WritableMappingsFlushed: true,
	}, releaseFn, nil
}

// KillServerForView forcibly closes the loopback NFSv3 server for viewID to simulate a mid-session server crash.
func (a *Adapter) KillServerForView(viewID string) error {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
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

// Release revokes private-git bindings, verifies mount ownership before unmounting each root,
// closes the loopback NFSv3 servers, and releases the layeredfs.Core view while preserving BackingDir.
func (a *Adapter) Release(ctx context.Context, viewID string) (adapter.ReleaseReceipt, error) {
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok {
		delete(a.views, viewID)
	}
	a.mu.Unlock()
	if !ok {
		return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonViewNotFound},
			&adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	var firstErr error
	for rootID, m := range st.mounts {
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
		alive := checkServerAlive(m) == nil
		if uErr := a.unmountOwnedRoot(ctx, m, alive); uErr != nil && firstErr == nil {
			firstErr = uErr
		}
		if m.server != nil {
			m.server.ReleaseExport(viewID)
			_ = m.server.Close()
		}
	}
	if cErr := st.core.ReleaseView(); cErr != nil && firstErr == nil {
		firstErr = &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "release", ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: cErr.Error(), Cause: cErr}
	}
	if firstErr != nil {
		return adapter.ReleaseReceipt{ViewID: viewID, AdapterID: adapter.AdapterNFSDarwin, Settled: false, ReasonCode: adapter.ReasonCleanupFailed, Diagnostic: firstErr.Error()}, firstErr
	}
	_ = os.RemoveAll(st.view.RuntimeViewDir)
	return adapter.ReleaseReceipt{
		ViewID:       viewID,
		AdapterID:    adapter.AdapterNFSDarwin,
		Settled:      true,
		ReasonCode:   adapter.ReasonNone,
		CleanedPaths: []string{st.view.RuntimeViewDir},
	}, nil
}

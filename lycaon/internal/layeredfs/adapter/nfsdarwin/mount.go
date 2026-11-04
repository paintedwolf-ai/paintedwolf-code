//go:build darwin

package nfsdarwin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// FormatMountOptions returns the explicit macOS /sbin/mount_nfs options for port.
func FormatMountOptions(port int) string {
	return fmt.Sprintf(
		"vers=3,tcp,port=%d,mountport=%d,locallocks,nodev,nosuid,nobrowse,%s,retrans=%d,timeo=%d,soft,rsize=%d,wsize=%d",
		port, port, DefaultCachePolicy, DefaultRetrans, DefaultTimeo, DefaultRSize, DefaultWSize,
	)
}

// Prepare creates the layeredfs.Core instance, starts a loopback NFSv3 server for each root,
// registers per-mount capability exports, and mounts them onto user-owned runtime directories.
func (a *Adapter) Prepare(ctx context.Context, req adapter.PrepareRequest) (*adapter.PreparedView, error) {
	if err := ctx.Err(); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	if a.cfg.HostOS != "darwin" {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonUnsupportedPlatform, Diagnostic: a.cfg.HostOS}
	}
	if strings.TrimSpace(req.ViewID) == "" || strings.TrimSpace(req.BackingDir) == "" || strings.TrimSpace(req.RuntimeDir) == "" {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "ViewID, BackingDir, and RuntimeDir are required"}
	}
	narrowed, err := adapter.ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	_, exists := a.views[req.ViewID]
	_, inProgress := a.preparing[req.ViewID]
	if !exists && !inProgress {
		a.preparing[req.ViewID] = struct{}{}
	}
	a.mu.Unlock()
	if exists || inProgress {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: "duplicate view " + req.ViewID}
	}
	defer func() {
		a.mu.Lock()
		delete(a.preparing, req.ViewID)
		a.mu.Unlock()
	}()

	absBacking, err := filepath.Abs(req.BackingDir)
	if err != nil {
		return nil, err
	}
	absRuntime, err := filepath.Abs(req.RuntimeDir)
	if err != nil {
		return nil, err
	}
	runtimeViewDir := filepath.Join(absRuntime, req.ViewID)
	mountsBase := filepath.Join(runtimeViewDir, "mounts")
	for _, dir := range []string{absBacking, runtimeViewDir, mountsBase} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
		}
	}

	coreRoots, primaryRoot, denyPaths, err := normalizeRootGrants(narrowed)
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, err
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:          req.ViewID,
		BackingDir:      absBacking,
		Roots:           coreRoots,
		ActiveRootID:    primaryRoot,
		ParentLayers:    req.ParentLayers,
		GitInterceptor:  req.GitInterceptor,
		ObservationSink: req.ObservationSink,
	})
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	mounts := make(map[string]*rootMount, len(narrowed))
	preparedRoots := make([]adapter.PreparedRoot, 0, len(narrowed))
	rollback := func() {
		for _, m := range mounts {
			_ = a.unmountOwnedRoot(context.Background(), m, true)
			if m.server != nil {
				m.server.ReleaseExport(req.ViewID)
				_ = m.server.Close()
			}
		}
		_ = core.ReleaseView()
		_ = a.removeRuntimeDir(runtimeViewDir)
	}

	for _, g := range narrowed {
		mountDir := filepath.Join(mountsBase, g.RootID)
		if custom := strings.TrimSpace(req.TargetRootDirs[g.RootID]); custom != "" {
			mountDir = filepath.Clean(custom)
		}
		rm, mErr := a.mountSingleRoot(ctx, req.ViewID, runtimeViewDir, g, mountDir, core)
		if mErr != nil {
			rollback()
			return nil, mErr
		}
		mounts[g.RootID] = rm
		preparedRoots = append(preparedRoots, adapter.PreparedRoot{
			RootID:        g.RootID,
			Label:         g.Label,
			SourcePath:    g.SourcePath,
			Subroot:       g.Subroot,
			WorkspacePath: rm.mountPath,
			IsPrimary:     g.RootID == primaryRoot,
		})
	}

	primaryMount := mounts[primaryRoot]
	prepView := &adapter.PreparedView{
		ViewID:          req.ViewID,
		AdapterID:       adapter.AdapterNFSDarwin,
		Facts:           a.facts,
		Core:            core,
		PrimaryRootID:   primaryRoot,
		PrimaryWorkPath: primaryMount.mountPath,
		Roots:           preparedRoots,
		BackingDir:      absBacking,
		RuntimeViewDir:  runtimeViewDir,
		TaskPathHints:   append([]string(nil), req.TaskPathHints...),
	}

	st := &viewState{
		view:            prepView,
		core:            core,
		primaryRoot:     primaryRoot,
		mounts:          mounts,
		denySourcePaths: denyPaths,
		quiesceHook:     req.QuiesceHook,
		flushHook:       req.FlushClientHook,
	}
	a.mu.Lock()
	a.views[req.ViewID] = st
	a.mu.Unlock()

	if mgr, ok := req.GitInterceptor.(*privategit.Manager); ok && mgr != nil {
		if bErr := a.BindPrivateGit(req.ViewID, req.ViewID, mgr); bErr != nil {
			_, _ = a.Release(context.Background(), req.ViewID)
			return nil, bErr
		}
	}
	return prepView, nil
}

func (a *Adapter) mountSingleRoot(
	ctx context.Context,
	viewID, runtimeViewDir string,
	grant adapter.RootGrant,
	mountDir string,
	core *layeredfs.Core,
) (*rootMount, error) {
	if err := os.MkdirAll(filepath.Dir(mountDir), 0o700); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	parentDev, err := statDeviceID(filepath.Dir(mountDir))
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	if err := os.MkdirAll(mountDir, 0o700); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	uid := uint32(os.Getuid())
	gid := uint32(os.Getgid())
	srv, err := nfsserver.New(core, nfsserver.Config{
		ListenAddr:    "127.0.0.1:0",
		ExpectedUID:   uid,
		ExpectedGID:   gid,
		MaxHandles:    a.cfg.MaxHandles,
		FaultInjector: a.cfg.FaultInjector,
	})
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonTransientStartupFailure, Diagnostic: err.Error(), Cause: err}
	}
	nfsserver.BindViewCore(srv, viewID, core, false)
	capCfg, err := srv.RegisterExportWithUID(viewID, grant.RootID, "", uid, gid)
	if err != nil {
		_ = srv.Close()
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	tcpAddr, ok := srv.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port <= 0 {
		_ = srv.Close()
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonTransientStartupFailure, Diagnostic: "invalid loopback TCP port"}
	}
	port := tcpAddr.Port
	mountOpts := FormatMountOptions(port)
	exportSource := "127.0.0.1:" + capCfg.ExportPath

	// The identity lands before the mount exists, so a crash inside this function leaves a record to
	// settle by; an orphan mount without one could never be attributed.
	canonMount := canonicalPath(mountDir)
	record, err := newConnectionRecord(viewID, grant.RootID, canonMount, exportSource)
	if err == nil {
		err = adapter.WriteRecordedConnection(runtimeViewDir, record)
	}
	if err != nil {
		srv.ReleaseExport(viewID)
		_ = srv.Close()
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	mCtx, cancel := context.WithTimeout(ctx, a.cfg.MountTimeout)
	defer cancel()
	out, mErr := runBoundedHelper(mCtx, "nfs_mount", a.cfg.MountNFSBin, "-o", mountOpts, exportSource, mountDir)
	if mErr != nil {
		srv.ReleaseExport(viewID)
		_ = srv.Close()
		reason := adapter.ReasonMountDenied
		if errors.Is(mCtx.Err(), context.DeadlineExceeded) || errors.Is(mCtx.Err(), context.Canceled) {
			reason = adapter.ReasonContextCanceled
		}
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterNFSDarwin,
			Op:         "prepare",
			ReasonCode: reason,
			Diagnostic: fmt.Sprintf("mount_nfs %s -> %s failed: %v (%s)", exportSource, mountDir, mErr, strings.TrimSpace(string(out))),
			Cause:      mErr,
		}
	}

	mountedDev, dErr := statDeviceID(mountDir)
	if dErr != nil || mountedDev == 0 || mountedDev == parentDev {
		fCtx, fCancel := context.WithTimeout(context.Background(), unmountWaitBound)
		_, _ = runBoundedHelper(fCtx, "nfs_unmount", a.cfg.UmountBin, "-f", mountDir)
		fCancel()
		srv.ReleaseExport(viewID)
		_ = srv.Close()
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterNFSDarwin,
			Op:         "prepare",
			ReasonCode: adapter.ReasonMountDenied,
			Diagnostic: fmt.Sprintf("mount_nfs did not change st_dev on %s (parent=%d mounted=%d err=%v)", mountDir, parentDev, mountedDev, dErr),
		}
	}

	obs, _ := srv.LastMountHandshake(capCfg.ExportPath)
	facts := MountFacts{
		ViewID:         viewID,
		RootID:         grant.RootID,
		MountID:        fmt.Sprintf("nfsdarwin:%s:%s:%d", viewID, grant.RootID, port),
		ConnectionID:   fmt.Sprintf("127.0.0.1:%d%s", port, capCfg.ExportPath),
		MountPath:      canonMount,
		ExportPath:     capCfg.ExportPath,
		ExportSource:   exportSource,
		ServerPort:     port,
		MountPort:      port,
		CachePolicy:    DefaultCachePolicy,
		RetryPolicy:    fmt.Sprintf("retrans=%d,soft", DefaultRetrans),
		Timeout:        fmt.Sprintf("timeo=%d", DefaultTimeo),
		RSize:          DefaultRSize,
		WSize:          DefaultWSize,
		MountOptions:   mountOpts,
		ParentDev:      parentDev,
		MountedDev:     mountedDev,
		HandshakeUID:   obs.ObservedUID,
		HandshakeGID:   obs.ObservedGID,
		AllowedDataUID: uid,
		AllowedDataGID: gid,
	}
	record.MountID, record.ConnectionID, record.DevMajorMinor = facts.MountID, facts.ConnectionID, formatDevice(mountedDev)
	if err := adapter.WriteRecordedConnection(runtimeViewDir, record); err != nil {
		fCtx, fCancel := context.WithTimeout(context.Background(), unmountWaitBound)
		_, _ = runBoundedHelper(fCtx, "nfs_unmount", a.cfg.UmountBin, "-f", mountDir)
		fCancel()
		srv.ReleaseExport(viewID)
		_ = srv.Close()
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	return &rootMount{
		grant:      grant,
		mountPath:  canonMount,
		server:     srv,
		capability: capCfg,
		facts:      facts,
	}, nil
}

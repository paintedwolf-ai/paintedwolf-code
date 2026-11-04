package fuselinux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// FormatMountOptions returns the explicit unprivileged fusermount3 options for (viewID, rootID).
// It never includes allow_other or allow_root. default_permissions has the kernel enforce file
// modes, so read-only files and access(2) behave for a worker as they do on a local filesystem.
func FormatMountOptions(fsName string) string {
	return fmt.Sprintf("rw,nosuid,nodev,default_permissions,fsname=%s,subtype=%s", fsName, DefaultSubtype)
}

func formatFSName(viewID, rootID string) string {
	s := fmt.Sprintf("lycaon-%s-%s", viewID, rootID)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (a *Adapter) preflightProbeChecks(ctx context.Context, probeCfg adapter.ProbeConfig, res *adapter.ProbeResult) error {
	if a.cfg.HostOS != "linux" {
		res.ReasonCode = adapter.ReasonUnsupportedPlatform
		res.Diagnostic = fmt.Sprintf("fuselinux requires linux host, got %s", a.cfg.HostOS)
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if strings.EqualFold(strings.TrimSpace(probeCfg.ConfinementContext), string(adapter.ReasonConfinementUnavailable)) {
		res.ReasonCode = adapter.ReasonConfinementUnavailable
		res.Diagnostic = "required mount confinement context is unavailable"
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if a.cfg.ConfinementCheckFn == nil {
		res.ReasonCode = adapter.ReasonConfinementUnavailable
		res.Diagnostic = "no confinement check is configured, so direct backing denial cannot be established"
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic}
	}
	if cErr := a.cfg.ConfinementCheckFn(ctx, probeCfg); cErr != nil {
		res.ReasonCode, res.Diagnostic = adapter.ReasonConfinementUnavailable, cErr.Error()
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: cErr}
	}
	res.BackingDenialVerified = true
	if err := checkFUSEDeviceAvailable(a.cfg.FuseDevicePath); err != nil {
		res.ReasonCode = adapter.ReasonFUSEDeviceUnavailable
		res.Diagnostic = fmt.Sprintf("FUSE device unavailable at %s: %v", a.cfg.FuseDevicePath, err)
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	if info, err := os.Stat(a.cfg.FusermountBin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		res.ReasonCode = adapter.ReasonFUSEHelperUnavailable
		res.Diagnostic = fmt.Sprintf("fusermount3 helper unavailable at %s: %v", a.cfg.FusermountBin, err)
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	return nil
}

// Prepare creates the layeredfs.Core instance, starts an in-process FUSE v7.31 server for each root,
// and mounts it onto an engine-owned worker directory via fusermount3 without allow_other.
func (a *Adapter) Prepare(ctx context.Context, req adapter.PrepareRequest) (*adapter.PreparedView, error) {
	if err := ctx.Err(); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	if a.cfg.HostOS != "linux" {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonUnsupportedPlatform, Diagnostic: a.cfg.HostOS}
	}
	if strings.TrimSpace(req.ViewID) == "" || strings.TrimSpace(req.BackingDir) == "" || strings.TrimSpace(req.RuntimeDir) == "" {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "ViewID, BackingDir, and RuntimeDir are required"}
	}
	narrowed, err := adapter.ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}

	a.mu.RLock()
	_, exists := a.views[req.ViewID]
	a.mu.RUnlock()
	if exists {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "duplicate view " + req.ViewID}
	}

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
			return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
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
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	mounts := make(map[string]*rootMount, len(narrowed))
	preparedRoots := make([]adapter.PreparedRoot, 0, len(narrowed))
	rollback := func() {
		for _, m := range mounts {
			_ = a.unmountOwnedRoot(context.Background(), m, true)
			if m.server != nil {
				_ = m.server.Close()
			}
		}
		_ = core.ReleaseView()
		_ = os.RemoveAll(runtimeViewDir)
	}

	for _, g := range narrowed {
		mountDir := filepath.Join(mountsBase, g.RootID)
		if custom := strings.TrimSpace(req.TargetRootDirs[g.RootID]); custom != "" {
			mountDir = filepath.Clean(custom)
		}
		rm, mErr := a.mountSingleRoot(ctx, req.ViewID, runtimeViewDir, g, mountDir, absBacking, core)
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
		AdapterID:       adapter.AdapterFUSELinux,
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
		dirtyPaths:      make(map[string]map[string]struct{}, len(narrowed)),
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
	backingDir string,
	core *layeredfs.Core,
) (*rootMount, error) {
	if err := checkFUSEDeviceAvailable(a.cfg.FuseDevicePath); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEDeviceUnavailable, Diagnostic: err.Error(), Cause: err}
	}
	if info, err := os.Stat(a.cfg.FusermountBin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEHelperUnavailable, Diagnostic: fmt.Sprintf("fusermount3 unavailable at %s: %v", a.cfg.FusermountBin, err), Cause: err}
	}
	if a.cfg.MountHookForTest != nil {
		if hErr := a.cfg.MountHookForTest(viewID, grant.RootID, mountDir); hErr != nil {
			return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: hErr.Error(), Cause: hErr}
		}
	}
	if err := os.MkdirAll(filepath.Dir(mountDir), 0o700); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	parentDev, err := statDeviceID(filepath.Dir(mountDir))
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	if err := os.MkdirAll(mountDir, 0o700); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	canonMount := canonicalPath(mountDir)
	fsName := formatFSName(viewID, grant.RootID)
	mountOpts := FormatMountOptions(fsName)
	uid := uint32(os.Getuid())
	gid := uint32(os.Getgid())

	// The identity lands before the mount exists, so a crash inside this function leaves a
	// record to settle by; an orphan mount without one could never be attributed.
	record, err := newConnectionRecord(viewID, grant.RootID, canonMount, fsName)
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	if err := adapter.WriteRecordedConnection(runtimeViewDir, record); err != nil {
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	srv, err := mountAndStartFUSEServer(ctx, a.cfg, ServerConfig{
		ViewID:                viewID,
		RootID:                grant.RootID,
		MountPath:             canonMount,
		FSName:                fsName,
		MountOptions:          mountOpts,
		OwnerUID:              uid,
		OwnerGID:              gid,
		MaxConcurrentRequests: a.cfg.MaxConcurrentRequests,
		MaxLockWaiters:        a.cfg.MaxLockWaiters,
		BackingDir:            backingDir,
		OnMutationHook:        a.cfg.OnMutationHook,
		OnLockWait:            a.cfg.OnLockWait,
	}, core)
	if err != nil {
		return nil, err
	}

	mountedDev, dErr := statDeviceID(canonMount)
	if dErr != nil || mountedDev == 0 || mountedDev == parentDev {
		_ = srv.Close()
		_ = runFusermountUnmount(context.Background(), a.cfg.FusermountBin, canonMount, true)
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterFUSELinux,
			Op:         "prepare",
			ReasonCode: adapter.ReasonFUSEMountDenied,
			Diagnostic: fmt.Sprintf("fusermount3 did not change st_dev on %s (parent=%d mounted=%d err=%v)", canonMount, parentDev, mountedDev, dErr),
		}
	}

	mInfo, mErr := verifyKernelMountInfo(canonMount, fsName, uid, gid)
	if mErr != nil {
		_ = srv.Close()
		_ = runFusermountUnmount(context.Background(), a.cfg.FusermountBin, canonMount, true)
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterFUSELinux,
			Op:         "prepare",
			ReasonCode: adapter.ReasonFUSEMountDenied,
			Diagnostic: mErr.Error(),
			Cause:      mErr,
		}
	}

	if pErr := srv.optOutOfFUSEPoll(canonMount); pErr != nil {
		_ = srv.Close()
		_ = runFusermountUnmount(context.Background(), a.cfg.FusermountBin, canonMount, true)
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: pErr.Error(), Cause: pErr}
	}

	// The stat above completed only after FUSE_INIT, so the exchange is recorded by now.
	protocol := srv.NegotiatedProtocol()
	if protocol == nil {
		_ = srv.Close()
		_ = runFusermountUnmount(context.Background(), a.cfg.FusermountBin, canonMount, true)
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "FUSE_INIT did not complete on " + canonMount}
	}
	facts := MountFacts{
		ViewID: viewID, RootID: grant.RootID,
		MountID:        fmt.Sprintf("fuselinux:%s:%s:%s", viewID, grant.RootID, mInfo.mountID),
		ConnectionID:   fmt.Sprintf("fuse:%s:%s", mInfo.devMajorMinor, fsName),
		MountPath:      canonMount,
		FSName:         fsName,
		Subtype:        DefaultSubtype,
		FilesystemType: mInfo.filesystem,
		DevMajorMinor:  mInfo.devMajorMinor,
		MountOptions:   mInfo.mountOptions,
		SuperOptions:   mInfo.superOptions,
		ProtocolMajor:  FUSEProtocolMajor, ProtocolMinor: FUSEProtocolMinor,
		KernelMinor: protocol.KernelMinor, GrantedFlags: protocol.GrantedFlags,
		MaxReadAhead: DefaultMaxReadAhead, MaxWrite: DefaultMaxWrite,
		CachePolicy: DefaultCachePolicy,
		ParentDev:   parentDev, MountedDev: mountedDev,
		OwnerUID: uid, OwnerGID: gid,
	}
	recordMountFacts(&record, facts)
	if err := adapter.WriteRecordedConnection(runtimeViewDir, record); err != nil {
		_ = srv.Close()
		_ = runFusermountUnmount(context.Background(), a.cfg.FusermountBin, canonMount, true)
		return nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	return &rootMount{grant: grant, mountPath: canonMount, server: srv, facts: facts}, nil
}

func normalizeRootGrants(grants []adapter.RootGrant) ([]layeredfs.RootSpec, string, []string, error) {
	coreRoots := make([]layeredfs.RootSpec, 0, len(grants))
	denyPaths := make([]string, 0, len(grants))
	var primaryRoot string
	for i, g := range grants {
		if strings.TrimSpace(g.RootID) == "" || strings.TrimSpace(g.SourcePath) == "" {
			return nil, "", nil, &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "prepare", ReasonCode: adapter.ReasonFUSEMountDenied, Diagnostic: "grant RootID and SourcePath are required"}
		}
		absTrunk, err := filepath.Abs(g.SourcePath)
		if err != nil {
			return nil, "", nil, err
		}
		isPrimary := g.IsPrimary || (primaryRoot == "" && i == 0)
		if isPrimary && primaryRoot == "" {
			primaryRoot = g.RootID
		}
		label := g.Label
		if label == "" {
			label = g.RootID
		}
		coreRoots = append(coreRoots, layeredfs.RootSpec{
			ID: g.RootID, Label: label, TrunkPath: absTrunk, Subroot: g.Subroot, IsPrimary: isPrimary,
		})
		denyPaths = append(denyPaths, absTrunk)
	}
	return coreRoots, primaryRoot, denyPaths, nil
}

func prepareProbeBaseDir(runtimeRoot string) (string, func(), error) {
	if strings.TrimSpace(runtimeRoot) != "" {
		if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
			return "", nil, err
		}
		d, err := os.MkdirTemp(runtimeRoot, "fuselinux-probe-*")
		if err != nil {
			return "", nil, err
		}
		return d, func() { _ = os.RemoveAll(d) }, nil
	}
	d, err := os.MkdirTemp("", "fuselinux-probe-*")
	if err != nil {
		return "", nil, err
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}

func classifyProbeError(err error) adapter.ReasonCode {
	var aErr *adapter.AdapterError
	if errors.As(err, &aErr) && aErr.ReasonCode != "" {
		return aErr.ReasonCode
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return adapter.ReasonContextCanceled
	}
	return adapter.ReasonFUSEMountDenied
}

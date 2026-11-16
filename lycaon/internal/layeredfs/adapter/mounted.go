package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// MountedAdapterConfig configures MountedAdapter, a test fixture with no kernel mount.
type MountedAdapterConfig struct {
	AdapterID                  AdapterID
	TargetOS                   string
	InvalidationVerified       bool
	NegativeLookupInvalidation bool
	DirCookieInvalidation      bool
	AttributionCompleteness    string
	MountProbeFn               func(ctx context.Context, probeDir, backingDir string) (ReasonCode, string, error)
	VerifyBackingDenialFn      func(ctx context.Context, backingDir string) (bool, error)
	MountViewFn                func(ctx context.Context, viewID string, roots []PreparedRoot) error
	UnmountViewFn              func(ctx context.Context, viewID string, runtimeDir string) error
	FlushClientFn              func(ctx context.Context, viewID string) error
}

type mountedViewState struct {
	view                   *PreparedView
	denySourcePaths        []string
	quiesceHook, flushHook func(ctx context.Context, viewID string) error
	accessCount            atomic.Int64
	mountCount             int64
}

// MountedAdapter implements Adapter over plain directories so mounted claim and recovery logic can
// run without a kernel mount. The real backends are the fuselinux and nfsdarwin packages.
type MountedAdapter struct {
	cfg   MountedAdapterConfig
	facts AdapterFacts
	mu    sync.RWMutex
	views map[string]*mountedViewState
}

// NewMountedAdapter constructs the fixture MountedAdapter from explicit configuration.
func NewMountedAdapter(cfg MountedAdapterConfig) *MountedAdapter {
	completeness := strings.TrimSpace(cfg.AttributionCompleteness)
	if completeness == "" {
		completeness = "kernel_cached_partial"
	}
	return &MountedAdapter{
		cfg: cfg,
		facts: AdapterFacts{
			AdapterID:                 cfg.AdapterID,
			PreparationKind:           PreparationMounted,
			UntouchedSourceVisibility: VisibilityLive,
			CacheRevalidation: CacheRevalidation{
				Mode:                       "strict_generation_revalidation",
				ExplicitInvalidation:       true,
				InvalidationVerified:       cfg.InvalidationVerified,
				NegativeLookupInvalidation: cfg.NegativeLookupInvalidation,
				DirCookieInvalidation:      cfg.DirCookieInvalidation,
				AttributeTTL:               0,
			},
			AttributionCompleteness: completeness,
			MetadataCapabilities: MetadataCapabilities{
				SupportsSymlinks:   true,
				SupportsHardLinks:  true,
				SupportsXattrs:     true,
				SupportsChmod:      true,
				SupportsTimestamps: true,
				SupportsPOSIXLocks: true,
				SupportsFlock:      true,
				SupportsFIFOOpen:   true,
			},
			SupportsPrivateGit:        true,
			SupportsCheckpointBarrier: true,
			BarrierHoldBound:          layeredfs.DefaultBarrierHoldBound,
			OtherUserAccess:           OtherUserAccessNone,
		},
		views: make(map[string]*mountedViewState),
	}
}

func (a *MountedAdapter) ID() AdapterID       { return a.cfg.AdapterID }
func (a *MountedAdapter) Facts() AdapterFacts { return a.facts }

// Prepare allocates one mounted worker view without recursively scanning lower repositories.
func (a *MountedAdapter) Prepare(ctx context.Context, req PrepareRequest) (*PreparedView, error) {
	narrowed, err := ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}
	runtimeViewDir, backingDir, err := initViewDirs(req.ViewID, req.RuntimeDir, req.BackingDir)
	if err != nil {
		return nil, &AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	rootSpecs, preparedRoots, denyPaths, primaryID, primaryPath, err := buildMountedRoots(runtimeViewDir, req.TargetRootDirs, narrowed, req.HiddenPaths, req.HiddenNames)
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, err
	}

	core, resumed, err := OpenViewCore(req, layeredfs.Config{
		ViewID:          req.ViewID,
		BackingDir:      backingDir,
		Roots:           rootSpecs,
		ActiveRootID:    primaryID,
		ParentLayers:    req.ParentLayers,
		GitInterceptor:  req.GitInterceptor,
		ObservationSink: req.ObservationSink,
	})
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, &AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	if a.cfg.MountViewFn != nil {
		if mErr := a.cfg.MountViewFn(ctx, req.ViewID, preparedRoots); mErr != nil {
			_ = core.ReleaseView()
			_ = os.RemoveAll(runtimeViewDir)
			return nil, &AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: mErr.Error(), Cause: mErr}
		}
	}

	view := &PreparedView{
		Resumed:         resumed,
		ViewID:          req.ViewID,
		AdapterID:       a.cfg.AdapterID,
		Facts:           a.facts,
		Roots:           preparedRoots,
		PrimaryRootID:   primaryID,
		PrimaryWorkPath: primaryPath,
		BackingDir:      backingDir,
		RuntimeViewDir:  runtimeViewDir,
		SourcePaths:     append([]string(nil), denyPaths...),
		TaskPathHints:   append([]string(nil), req.TaskPathHints...),
		Core:            core,
	}

	a.mu.Lock()
	a.views[req.ViewID] = &mountedViewState{
		view:            view,
		denySourcePaths: denyPaths,
		quiesceHook:     req.QuiesceHook,
		flushHook:       req.FlushClientHook,
		mountCount:      1,
	}
	a.mu.Unlock()
	return view, nil
}

func buildMountedRoots(runtimeViewDir string, targetDirs map[string]string, narrowed []RootGrant, hidden, hiddenNames []string) ([]layeredfs.RootSpec, []PreparedRoot, []string, string, string, error) {
	var specs []layeredfs.RootSpec
	var prepared []PreparedRoot
	var denyPaths []string
	var primaryID, primaryPath string

	for _, g := range narrowed {
		mntPath := filepath.Join(runtimeViewDir, "mounts", g.RootID)
		if custom := strings.TrimSpace(targetDirs[g.RootID]); custom != "" {
			mntPath = filepath.Clean(custom)
		}
		if err := os.MkdirAll(mntPath, 0o750); err != nil {
			return nil, nil, nil, "", "", err
		}
		specs = append(specs, layeredfs.RootSpec{
			ID:          g.RootID,
			Label:       g.Label,
			TrunkPath:   g.SourcePath,
			Subroot:     g.Subroot,
			IsPrimary:   g.IsPrimary,
			Hidden:      HiddenRelsUnder(g.SourcePath, hidden),
			HiddenNames: hiddenNames,
		})
		prepared = append(prepared, PreparedRoot{
			RootID:        g.RootID,
			Label:         g.Label,
			SourcePath:    g.SourcePath,
			Subroot:       g.Subroot,
			WorkspacePath: mntPath,
			IsPrimary:     g.IsPrimary,
		})
		denyPaths = append(denyPaths, g.SourcePath)
		if g.IsPrimary && primaryID == "" {
			primaryID = g.RootID
			primaryPath = mntPath
		}
	}
	return specs, prepared, denyPaths, primaryID, primaryPath, nil
}

// CommandAccess supplies mounted paths and confinement facts without remounting per command.
func (a *MountedAdapter) CommandAccess(viewID string) (CommandAccessSpec, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return CommandAccessSpec{}, &AdapterError{AdapterID: a.cfg.AdapterID, Op: "command_access", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	count := st.accessCount.Add(1)
	rootPaths := make(map[string]string, len(st.view.Roots))
	for _, r := range st.view.Roots {
		rootPaths[r.RootID] = r.WorkspacePath
	}
	return CommandAccessSpec{
		ViewID:           viewID,
		AdapterID:        a.cfg.AdapterID,
		PrimaryRootID:    st.view.PrimaryRootID,
		WorkDir:          st.view.PrimaryWorkPath,
		RootPaths:        rootPaths,
		HiddenBackingDir: st.view.BackingDir,
		DenyDirectSource: append([]string(nil), st.denySourcePaths...),
		Facts:            a.facts,
		AccessCount:      count,
	}, nil
}

// CheckpointBarrier stops new mutations, quiesces writers, drains operations, and flushes client dirty data.
func (a *MountedAdapter) CheckpointBarrier(ctx context.Context, viewID string) (BarrierReceipt, func() error, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return BarrierReceipt{}, nil, &AdapterError{AdapterID: a.cfg.AdapterID, Op: "checkpoint_barrier", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	return executeCheckpointBarrier(ctx, a.cfg.AdapterID, st.view.Core, st.quiesceHook, combineFlushHooks(a.cfg.FlushClientFn, st.flushHook))
}

// IsViewActive reports whether the fixture still holds viewID.
func (a *MountedAdapter) IsViewActive(viewID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, live := a.views[viewID]
	return live
}

// BindPrivateGit binds the fixture's primary directory to mgr. Its mount identity names a fixture.
func (a *MountedAdapter) BindPrivateGit(viewID, workerID string, mgr *privategit.Manager, authorizedGitDirs ...string) error {
	if mgr == nil {
		return errors.New("adapter: nil privategit.Manager")
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return &AdapterError{AdapterID: a.cfg.AdapterID, Op: "bind_privategit", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	bindingFor := func(r PreparedRoot) privategit.MountedViewBinding {
		return privategit.MountedViewBinding{
			ViewID: viewID, WorkerID: workerID, RootID: r.RootID,
			TrunkDir: r.SourcePath, MountPath: r.WorkspacePath, AdapterID: string(a.cfg.AdapterID),
			Core: st.view.Core, MountID: "fixture:" + viewID, Active: true,
			IsActive: func() bool { return a.IsViewActive(viewID) },
		}
	}
	for _, r := range st.view.Roots {
		if r.RootID != st.view.PrimaryRootID {
			continue
		}
		if err := mgr.MountedClaimWithBinding(bindingFor(r), authorizedGitDirs...); err != nil {
			return err
		}
	}
	for _, r := range st.view.Roots {
		if r.RootID == st.view.PrimaryRootID {
			continue
		}
		if err := mgr.BindSecondaryRoot(bindingFor(r), authorizedGitDirs...); err != nil {
			return err
		}
	}
	return nil
}

// Release retires handles, settles the filesystem connection, and cleans up runtime directories.
func (a *MountedAdapter) Release(ctx context.Context, viewID string) (ReleaseReceipt, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return ReleaseReceipt{ViewID: viewID, AdapterID: a.cfg.AdapterID, Settled: false, ReasonCode: ReasonViewNotFound},
			&AdapterError{AdapterID: a.cfg.AdapterID, Op: "release", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	receipt, err := settleViewRelease(ctx, a.cfg.AdapterID, st.view, a.cfg.UnmountViewFn)
	if err == nil && receipt.Settled {
		a.mu.Lock()
		delete(a.views, viewID)
		a.mu.Unlock()
	}
	return receipt, err
}

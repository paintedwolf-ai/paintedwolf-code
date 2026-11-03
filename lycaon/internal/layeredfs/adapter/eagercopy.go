package adapter

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

type eagerViewState struct {
	view            *PreparedView
	seedRecords     map[layeredfs.QualifiedPath]layeredfs.BaseCapture
	denySourcePaths []string
	quiesceHook     func(ctx context.Context, viewID string) error
	flushHook       func(ctx context.Context, viewID string) error
	accessCount     atomic.Int64
}

// EagerCopyAdapter implements the bounded materialized fallback workspace adapter (#1658).
type EagerCopyAdapter struct {
	facts AdapterFacts
	mu    sync.RWMutex
	views map[string]*eagerViewState
}

// NewEagerCopyAdapter constructs a materialized EagerCopyAdapter with an optional FallbackReason.
func NewEagerCopyAdapter(fallbackReason ReasonCode) *EagerCopyAdapter {
	return &EagerCopyAdapter{
		facts: AdapterFacts{
			AdapterID:                 AdapterEagerCopy,
			PreparationKind:           PreparationMaterialized,
			UntouchedSourceVisibility: VisibilityFixed,
			CacheRevalidation: CacheRevalidation{
				Mode:                 "fixed_private_seed",
				ExplicitInvalidation: false,
				AttributeTTL:         0,
			},
			MetadataCapabilities: MetadataCapabilities{
				SupportsSymlinks:   true,
				SupportsHardLinks:  true,
				SupportsXattrs:     true,
				SupportsChmod:      true,
				SupportsTimestamps: true,
				SupportsPOSIXLocks: true,
				SupportsFlock:      true,
			},
			SupportsPrivateGit:        true,
			SupportsCheckpointBarrier: true,
			FallbackReason:            fallbackReason,
		},
		views: make(map[string]*eagerViewState),
	}
}

// WithFallbackReason returns an EagerCopyAdapter carrying the specified FallbackReason.
func (a *EagerCopyAdapter) WithFallbackReason(reason ReasonCode) *EagerCopyAdapter {
	a.mu.Lock()
	defer a.mu.Unlock()
	clone := NewEagerCopyAdapter(reason)
	clone.views = a.views
	return clone
}

func (a *EagerCopyAdapter) ID() AdapterID       { return AdapterEagerCopy }
func (a *EagerCopyAdapter) Facts() AdapterFacts { return a.facts }

// Probe verifies private seed creation, read, write, backing confinement, and cleanup.
func (a *EagerCopyAdapter) Probe(ctx context.Context, probeCfg ProbeConfig) (ProbeResult, error) {
	res := ProbeResult{AdapterID: AdapterEagerCopy, Attempts: 1}
	if err := ctx.Err(); err != nil {
		res.ReasonCode = ReasonContextCanceled
		res.Diagnostic = err.Error()
		return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	probeRoot, err := os.MkdirTemp(probeCfg.RuntimeDir, "lycaon-eager-probe-*")
	if err != nil {
		res.ReasonCode = ReasonMountDenied
		res.Diagnostic = err.Error()
		return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	srcDir := filepath.Join(probeRoot, "src")
	backingDir := filepath.Join(probeRoot, "backing")
	for _, d := range []string{srcDir, backingDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(probeRoot)
			res.ReasonCode = ReasonMountDenied
			res.Diagnostic = err.Error()
			return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
		}
	}
	res.MountExercised = true
	if err := exerciseProbeReadWrite(srcDir, backingDir, &res); err != nil {
		_ = os.RemoveAll(probeRoot)
		return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	res.BackingDenialVerified = true
	if err := os.RemoveAll(probeRoot); err != nil {
		res.ReasonCode = ReasonCleanupFailed
		res.Diagnostic = err.Error()
		return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
	}
	res.CleanedUp = true
	res.Supported = true
	return res, nil
}

// Prepare materializes selected roots into private storage within configured limits and registers seed bases.
func (a *EagerCopyAdapter) Prepare(ctx context.Context, req PrepareRequest) (*PreparedView, error) {
	narrowed, err := ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}
	maxEntries, maxBytes := resolvePrepareLimits(req.MaxEntries, req.MaxBytes)
	selectedIDs := collectGrantIDs(narrowed)

	if err := checkSourceIndexEstimates(req.ViewID, narrowed, selectedIDs, maxEntries, maxBytes); err != nil {
		return nil, err
	}

	runtimeViewDir, backingDir, err := initViewDirs(req.ViewID, req.RuntimeDir, req.BackingDir)
	if err != nil {
		return nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	outcome, err := seedSelectedRoots(ctx, req, runtimeViewDir, narrowed, selectedIDs, maxEntries, maxBytes)
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		if req.BackingDir != "" {
			_ = os.RemoveAll(req.BackingDir)
		}
		if limErr, ok := err.(*PreparationLimitError); ok {
			limErr.CleanedIncompleteSeed = true
		}
		return nil, err
	}

	gitInterceptor := req.GitInterceptor
	if gitInterceptor == nil && len(outcome.gitInterceptMap) > 0 {
		gitInterceptor = &seededGitInterceptor{redirects: outcome.gitInterceptMap}
	}

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:          req.ViewID,
		BackingDir:      backingDir,
		Roots:           outcome.rootSpecs,
		ActiveRootID:    outcome.primaryID,
		ParentLayers:    req.ParentLayers,
		GitInterceptor:  gitInterceptor,
		ObservationSink: req.ObservationSink,
	})
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	seedMap := make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture, len(outcome.bases))
	for _, b := range outcome.bases {
		if rErr := core.RegisterSeedBase(b); rErr != nil {
			_ = core.ReleaseView()
			_ = os.RemoveAll(runtimeViewDir)
			return nil, rErr
		}
		seedMap[b.Path] = b
	}

	view := &PreparedView{
		ViewID:          req.ViewID,
		AdapterID:       AdapterEagerCopy,
		Facts:           a.facts,
		Roots:           outcome.preparedRoots,
		PrimaryRootID:   outcome.primaryID,
		PrimaryWorkPath: outcome.primaryPath,
		BackingDir:      backingDir,
		RuntimeViewDir:  runtimeViewDir,
		TaskPathHints:   append([]string(nil), req.TaskPathHints...),
		SeededGitRepos:  outcome.seededRepos,
		ObservedEntries: outcome.observedEntries,
		ObservedBytes:   outcome.observedBytes,
		Core:            core,
	}

	a.mu.Lock()
	a.views[req.ViewID] = &eagerViewState{
		view:            view,
		seedRecords:     seedMap,
		denySourcePaths: outcome.denyPaths,
		quiesceHook:     req.QuiesceHook,
		flushHook:       req.FlushClientHook,
	}
	a.mu.Unlock()
	return view, nil
}

// CommandAccess returns the seeded private paths and confinement facts without re-copying.
func (a *EagerCopyAdapter) CommandAccess(viewID string) (CommandAccessSpec, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return CommandAccessSpec{}, &AdapterError{AdapterID: AdapterEagerCopy, Op: "command_access", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	count := st.accessCount.Add(1)
	rootPaths := make(map[string]string, len(st.view.Roots))
	for _, r := range st.view.Roots {
		rootPaths[r.RootID] = r.WorkspacePath
	}
	return CommandAccessSpec{
		ViewID:           viewID,
		AdapterID:        AdapterEagerCopy,
		PrimaryRootID:    st.view.PrimaryRootID,
		WorkDir:          st.view.PrimaryWorkPath,
		RootPaths:        rootPaths,
		HiddenBackingDir: st.view.BackingDir,
		DenyDirectSource: append([]string(nil), st.denySourcePaths...),
		Facts:            a.facts,
		AccessCount:      count,
	}, nil
}

// CheckpointBarrier quiesces writers, reconciles direct materialized edits, and acquires the Core barrier.
func (a *EagerCopyAdapter) CheckpointBarrier(ctx context.Context, viewID string) (BarrierReceipt, func() error, error) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return BarrierReceipt{}, nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "checkpoint_barrier", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	quiesceAndSync := func(cCtx context.Context, id string) error {
		if st.quiesceHook != nil {
			if err := st.quiesceHook(cCtx, id); err != nil {
				return err
			}
		}
		return syncMaterializedTreeToCore(st.view, st.seedRecords)
	}
	return executeCheckpointBarrier(ctx, AdapterEagerCopy, st.view.Core, quiesceAndSync, st.flushHook)
}

// Release retires handles and cleans up the materialized view's runtime resources.
func (a *EagerCopyAdapter) Release(ctx context.Context, viewID string) (ReleaseReceipt, error) {
	a.mu.Lock()
	st, ok := a.views[viewID]
	if ok {
		delete(a.views, viewID)
	}
	a.mu.Unlock()
	if !ok {
		return ReleaseReceipt{ViewID: viewID, AdapterID: AdapterEagerCopy, Settled: false, ReasonCode: ReasonViewNotFound},
			&AdapterError{AdapterID: AdapterEagerCopy, Op: "release", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	return settleViewRelease(ctx, AdapterEagerCopy, st.view, nil)
}

package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

var sharedEagerViews sync.Map // map[string]*eagerViewState

type eagerViewState struct {
	view            *PreparedView
	capture         *statCapture
	syncMu          sync.Mutex
	denySourcePaths []string
	gitStore        *privategit.RetainedObjectStore
	gitLeases       []string
	quiesceHook     func(ctx context.Context, viewID string) error
	flushHook       func(ctx context.Context, viewID string) error
	accessCount     atomic.Int64
}

// EagerCopyAdapter materializes a working tree and a pristine tree as its exact base (#1658).
type EagerCopyAdapter struct {
	facts     AdapterFacts
	mu        sync.RWMutex
	views     map[string]*eagerViewState
	gitStores *gitStoreRegistry
}

// NewEagerCopyAdapter constructs a materialized EagerCopyAdapter. FallbackReason names the lasting
// reason a mount could not serve an over-budget worker, when that is why this adapter was chosen.
func NewEagerCopyAdapter(fallbackReason ReasonCode) *EagerCopyAdapter {
	return &EagerCopyAdapter{
		facts: AdapterFacts{
			AdapterID:                 AdapterEagerCopy,
			PreparationKind:           PreparationMaterialized,
			UntouchedSourceVisibility: VisibilityFixed,
			CacheRevalidation: CacheRevalidation{
				Mode:                 "fixed_private_seed",
				ExplicitInvalidation: false,
				InvalidationVerified: false,
				AttributeTTL:         0,
			},
			AttributionCompleteness: "materialized_snapshot",
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
			FallbackReason:            fallbackReason,
		},
		views:     make(map[string]*eagerViewState),
		gitStores: newGitStoreRegistry(),
	}
}

// WithFallbackReason returns an EagerCopyAdapter carrying the specified FallbackReason.
func (a *EagerCopyAdapter) WithFallbackReason(reason ReasonCode) *EagerCopyAdapter {
	a.mu.Lock()
	defer a.mu.Unlock()
	clone := NewEagerCopyAdapter(reason)
	clone.views = a.views
	clone.gitStores = a.gitStores
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
	if strings.TrimSpace(probeCfg.RuntimeDir) != "" {
		if err := os.MkdirAll(probeCfg.RuntimeDir, 0o700); err != nil {
			res.ReasonCode = ReasonMountDenied
			res.Diagnostic = err.Error()
			return res, &AdapterError{AdapterID: AdapterEagerCopy, Op: "probe", ReasonCode: res.ReasonCode, Diagnostic: res.Diagnostic, Cause: err}
		}
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

// Prepare builds both trees for the selected roots and writes the stat manifest. Nothing is hashed.
// Every failure, including a budget overrun, removes only what this preparation made.
func (a *EagerCopyAdapter) Prepare(ctx context.Context, req PrepareRequest) (*PreparedView, error) {
	narrowed, err := ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}
	runtimeViewDir, backingDir, err := initViewDirs(req.ViewID, req.RuntimeDir, req.BackingDir)
	if err != nil {
		return nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	pristineDir := strings.TrimSpace(req.PristineDir)
	if pristineDir == "" {
		pristineDir = filepath.Join(backingDir, "pristine")
	}
	manifestPath := strings.TrimSpace(req.StatManifestPath)
	if manifestPath == "" {
		manifestPath = filepath.Join(backingDir, "stat-manifest")
	}

	outcome, err := seedSelectedRoots(ctx, req, runtimeViewDir, pristineDir, narrowed, a.gitStores)
	if err != nil {
		cleanupFailedPrepare(req, runtimeViewDir, pristineDir, manifestPath, outcome, nil)
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
		cleanupFailedPrepare(req, runtimeViewDir, pristineDir, manifestPath, outcome, nil)
		return nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "prepare", ReasonCode: ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}
	for _, r := range outcome.preparedRoots {
		if err := core.RegisterLowerProvider(r.RootID, newPristineLowerProvider(r.RootID, r.PristinePath, outcome.records)); err != nil {
			cleanupFailedPrepare(req, runtimeViewDir, pristineDir, manifestPath, outcome, core)
			return nil, err
		}
	}

	takenNS := time.Now().UnixNano()
	header := statManifestHeader{Format: StatManifestFormat, ViewID: req.ViewID, TakenNS: takenNS, Roots: manifestRootsOf(outcome.preparedRoots)}
	if err := writeStatManifest(manifestPath, header, outcome.records); err != nil {
		cleanupFailedPrepare(req, runtimeViewDir, pristineDir, manifestPath, outcome, core)
		return nil, err
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
		ObservedEntries: outcome.stats.Entries,
		ObservedBytes:   outcome.logicalBytes,
		Selection:       req.SelectionFacts,
		Materialize:     outcome.stats,
		Core:            core,
	}
	a.adopt(view, &eagerViewState{
		view:            view,
		capture:         newStatCapture(takenNS, outcome.records, outcome.preparedRoots, req.ParentLayers, backingDir),
		denySourcePaths: outcome.denyPaths,
		gitStore:        outcome.gitStore,
		gitLeases:       outcome.gitLeases,
		quiesceHook:     req.QuiesceHook,
		flushHook:       req.FlushClientHook,
	})
	return view, nil
}

func (a *EagerCopyAdapter) adopt(view *PreparedView, st *eagerViewState) {
	a.mu.Lock()
	a.views[view.ViewID] = st
	a.mu.Unlock()
	sharedEagerViews.Store(view.ViewID, st)
}

func manifestRootsOf(roots []PreparedRoot) []manifestRoot {
	out := make([]manifestRoot, 0, len(roots))
	for _, r := range roots {
		out = append(out, manifestRoot{
			RootID: r.RootID, Label: r.Label, SourcePath: r.SourcePath, Subroot: r.Subroot,
			WorkspacePath: r.WorkspacePath, PristinePath: r.PristinePath, IsPrimary: r.IsPrimary,
		})
	}
	return out
}

// ReopenRequest names a materialized view an earlier engine process prepared.
type ReopenRequest struct {
	ViewID           string
	RuntimeDir       string
	BackingDir       string
	StatManifestPath string
	ObservationSink  layeredfs.ObservationSink
}

// Reopen rebuilds a materialized view's Core from its stat manifest and pristine tree. The next
// capture re-derives the worker's changes the same way any capture does, so nothing else is needed.
func (a *EagerCopyAdapter) Reopen(ctx context.Context, req ReopenRequest) (*PreparedView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	header, records, err := loadStatManifest(req.StatManifestPath)
	if err != nil {
		return nil, err
	}
	runtimeViewDir, backingDir, err := initViewDirs(req.ViewID, req.RuntimeDir, req.BackingDir)
	if err != nil {
		return nil, err
	}
	roots := make([]PreparedRoot, 0, len(header.Roots))
	specs := make([]layeredfs.RootSpec, 0, len(header.Roots)+1)
	primaryID, primaryPath := "", ""
	var deny []string
	for _, r := range header.Roots {
		roots = append(roots, PreparedRoot{
			RootID: r.RootID, Label: r.Label, SourcePath: r.SourcePath, Subroot: r.Subroot,
			WorkspacePath: r.WorkspacePath, PristinePath: r.PristinePath, IsPrimary: r.IsPrimary,
		})
		specs = append(specs, layeredfs.RootSpec{ID: r.RootID, Label: r.Label, TrunkPath: r.WorkspacePath, Subroot: r.Subroot, IsPrimary: r.IsPrimary})
		deny = append(deny, r.SourcePath)
		if r.IsPrimary {
			primaryID, primaryPath = r.RootID, r.WorkspacePath
		}
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: req.ViewID, BackingDir: backingDir, Roots: specs, ActiveRootID: primaryID, ObservationSink: req.ObservationSink,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range roots {
		if err := core.RegisterLowerProvider(r.RootID, newPristineLowerProvider(r.RootID, r.PristinePath, records)); err != nil {
			_ = core.ReleaseView()
			return nil, err
		}
	}
	view := &PreparedView{
		ViewID: req.ViewID, AdapterID: AdapterEagerCopy, Facts: a.facts, Roots: roots, PrimaryRootID: primaryID,
		PrimaryWorkPath: primaryPath, BackingDir: backingDir, RuntimeViewDir: runtimeViewDir, Core: core,
	}
	a.adopt(view, &eagerViewState{view: view, capture: newStatCapture(header.TakenNS, records, roots, nil, backingDir), denySourcePaths: deny})
	return view, nil
}

// LoadedView returns a view this process already holds.
func (a *EagerCopyAdapter) LoadedView(viewID string) (*PreparedView, bool) {
	st, ok := a.lookupViewState(viewID)
	if !ok {
		return nil, false
	}
	return st.view, true
}

// ChangedPaths lists what differs from the exact base now, reading only files whose stat differs.
func (a *EagerCopyAdapter) ChangedPaths(ctx context.Context, viewID string) ([]layeredfs.QualifiedPath, CaptureStats, error) {
	st, ok := a.lookupViewState(viewID)
	if !ok {
		return nil, CaptureStats{}, &AdapterError{AdapterID: AdapterEagerCopy, Op: "changed_paths", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	st.syncMu.Lock()
	defer st.syncMu.Unlock()
	paths, err := st.capture.changedPaths(ctx, st.view.Roots)
	return paths, st.capture.stats, err
}

// LastCaptureStats reports what the view's most recent capture or change listing did.
func (a *EagerCopyAdapter) LastCaptureStats(viewID string) (CaptureStats, bool) {
	st, ok := a.lookupViewState(viewID)
	if !ok {
		return CaptureStats{}, false
	}
	st.syncMu.Lock()
	defer st.syncMu.Unlock()
	return st.capture.stats, true
}

func (a *EagerCopyAdapter) lookupViewState(viewID string) (*eagerViewState, bool) {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if ok {
		return st, true
	}
	if v, ok := sharedEagerViews.Load(viewID); ok {
		return v.(*eagerViewState), true
	}
	return nil, false
}

// CommandAccess returns the seeded private paths and confinement facts without re-copying.
func (a *EagerCopyAdapter) CommandAccess(viewID string) (CommandAccessSpec, error) {
	st, ok := a.lookupViewState(viewID)
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

// CheckpointBarrier quiesces writers, captures the tree by stat, and acquires the Core barrier.
func (a *EagerCopyAdapter) CheckpointBarrier(ctx context.Context, viewID string) (BarrierReceipt, func() error, error) {
	st, ok := a.lookupViewState(viewID)
	if !ok {
		return BarrierReceipt{}, nil, &AdapterError{AdapterID: AdapterEagerCopy, Op: "checkpoint_barrier", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	quiesceAndSync := func(cCtx context.Context, id string) error {
		if st.quiesceHook != nil {
			if err := st.quiesceHook(cCtx, id); err != nil {
				return err
			}
		}
		st.syncMu.Lock()
		defer st.syncMu.Unlock()
		return st.capture.capture(cCtx, st.view)
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
		if v, loaded := sharedEagerViews.LoadAndDelete(viewID); loaded {
			st, ok = v.(*eagerViewState), true
		}
	} else {
		sharedEagerViews.Delete(viewID)
	}
	if !ok {
		return ReleaseReceipt{ViewID: viewID, AdapterID: AdapterEagerCopy, Settled: false, ReasonCode: ReasonViewNotFound},
			&AdapterError{AdapterID: AdapterEagerCopy, Op: "release", ReasonCode: ReasonViewNotFound, Diagnostic: viewID}
	}
	releaseGitLeases(st.gitStore, st.gitLeases)
	return settleViewRelease(ctx, AdapterEagerCopy, st.view, nil)
}

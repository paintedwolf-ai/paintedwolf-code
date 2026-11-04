package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

var sharedBranchViews sync.Map // map[string]branchViewState

// ErrWorkspaceStorageExhausted identifies a branch or seed write that failed
// because the central application-data volume cannot accept more data.
var ErrWorkspaceStorageExhausted = errors.New("worker workspace storage exhausted")

var queryAvailableStorageBytes = availableStorageBytes

func classifyProvisionError(err error) error {
	var capacityErr *CapacityError
	if err != nil && (isStorageExhausted(err) || errors.As(err, &capacityErr)) {
		return errors.Join(ErrWorkspaceStorageExhausted, err)
	}
	return err
}

// CapacityError reports insufficient bridge-cache capacity.
type CapacityError struct {
	Path      string
	Required  uint64
	Available uint64
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("worker seed needs %d bytes but only %d bytes are available on %s", e.Required, e.Available, e.Path)
}

// ProvisionStrategy is the storage path selected from successful clone probes.
type ProvisionStrategy string

const (
	ProvisionDirectCoW  ProvisionStrategy = "direct_cow"
	ProvisionBridgeCoW  ProvisionStrategy = "bridge_cow"
	ProvisionDirectCopy ProvisionStrategy = "direct_copy"
)

// PreparationProgress is ephemeral worker-claim progress. TotalBytes is known
// only after a bridge source survey has completed.
type PreparationProgress struct {
	Strategy   ProvisionStrategy
	Stage      string
	Files      int64
	Bytes      int64
	TotalBytes int64
}

type (
	preparationReporterKey struct{}
	workspaceSelectionKey  struct{}
	workspaceLimitsKey     struct{}
)

type workspaceSelectionContext struct {
	selection     adapter.WorkspaceSelection
	taskPathHints []string
}

type workspaceLimitsContext struct {
	maxEntries int64
	maxBytes   int64
}

const preparationReportInterval = 250 * time.Millisecond

// WithPreparationReporter attaches a worker-visible progress sink to a claim.
func WithPreparationReporter(ctx context.Context, report func(PreparationProgress)) context.Context {
	if report == nil {
		return ctx
	}
	var mu sync.Mutex
	var lastReport time.Time
	lastStage := ""
	throttled := func(progress PreparationProgress) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if progress.Stage == lastStage && now.Sub(lastReport) < preparationReportInterval {
			return
		}
		lastStage = progress.Stage
		lastReport = now
		report(progress)
	}
	return context.WithValue(ctx, preparationReporterKey{}, throttled)
}

// WithWorkspaceSelection attaches an explicit WorkspaceSelection and optional informational
// task path hints to a workspace provisioning context (#1658).
func WithWorkspaceSelection(ctx context.Context, selection adapter.WorkspaceSelection, taskPathHints ...string) context.Context {
	return context.WithValue(ctx, workspaceSelectionKey{}, workspaceSelectionContext{
		selection:     selection,
		taskPathHints: append([]string(nil), taskPathHints...),
	})
}

type workspaceProjectKey struct{}

// WithWorkspaceProject names the project a worker view belongs to.
func WithWorkspaceProject(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, workspaceProjectKey{}, strings.TrimSpace(projectID))
}

func workspaceProjectFromContext(ctx context.Context, fallback string) string {
	if id, ok := ctx.Value(workspaceProjectKey{}).(string); ok && id != "" {
		return id
	}
	return fallback
}

// WithWorkspaceLimits overrides the maximum entries and bytes permitted for a workspace preparation call (#1658).
func WithWorkspaceLimits(ctx context.Context, maxEntries, maxBytes int64) context.Context {
	return context.WithValue(ctx, workspaceLimitsKey{}, workspaceLimitsContext{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	})
}

func workspaceSelectionFromContext(ctx context.Context) (adapter.WorkspaceSelection, []string, bool) {
	val, ok := ctx.Value(workspaceSelectionKey{}).(workspaceSelectionContext)
	if !ok {
		return adapter.WorkspaceSelection{}, nil, false
	}
	return val.selection, val.taskPathHints, true
}

func reportPreparation(ctx context.Context, progress PreparationProgress) {
	if report, ok := ctx.Value(preparationReporterKey{}).(func(PreparationProgress)); ok {
		report(progress)
	}
}

// Binding is one isolated worker sandbox — a copy of the project tree living
// outside the repo. There is no git interaction and no linked worktree.
type Binding struct {
	ID        string
	Root      string
	Selection WorkspaceStrategySelection
	View      *adapter.PreparedView
	Adapter   adapter.Adapter
}

type branchViewState struct {
	selection WorkspaceStrategySelection
	views     []*adapter.PreparedView
}

// Manager creates and destroys per-job worker sandboxes, retaining a central
// bridge only when it enables copy-on-write branches.
type Manager struct {
	branchRoot string
	seedRoot   string
	locks      seedLockSet
	maxEntries int64
	maxBytes   int64
	selector   *adapter.Selector
	recovery   *recovery.Coordinator
	viewsMu    sync.Mutex
	views      map[string]branchViewState
	privateGit *privategit.Manager

	layeredOnce sync.Once
	services    *layeredServices
	servicesErr error
}

// NewManager returns a workspace manager rooted at out-of-repo branch and seed bases.
func NewManager(branchRoot, seedRoot string) *Manager {
	return &Manager{
		branchRoot: strings.TrimSpace(branchRoot),
		seedRoot:   strings.TrimSpace(seedRoot),
		locks:      newSeedLockSet(),
		maxEntries: adapter.DefaultMaxEntries,
		maxBytes:   adapter.DefaultMaxBytes,
		views:      make(map[string]branchViewState),
	}
}

// WithRecovery settles and rebuilds this manager's worker views through coord.
func (m *Manager) WithRecovery(coord *recovery.Coordinator) *Manager {
	m.recovery = coord
	return m
}

// WithSelector configures an explicit workspace adapter Selector on the Manager (#1658).
func (m *Manager) WithSelector(selector *adapter.Selector) *Manager {
	m.selector = selector
	return m
}

func (m *Manager) recordBranchViews(branchRoot string, sel WorkspaceStrategySelection, views ...*adapter.PreparedView) {
	m.viewsMu.Lock()
	defer m.viewsMu.Unlock()
	if m.views == nil {
		m.views = make(map[string]branchViewState)
	}
	clean := filepath.Clean(branchRoot)
	st := branchViewState{
		selection: sel,
		views:     append([]*adapter.PreparedView(nil), views...),
	}
	m.views[clean] = st
	sharedBranchViews.Store(clean, st)
}

func (m *Manager) lookupBranchViews(branchRoot string) (WorkspaceStrategySelection, *adapter.PreparedView, bool) {
	m.viewsMu.Lock()
	defer m.viewsMu.Unlock()
	st, ok := m.views[filepath.Clean(branchRoot)]
	if !ok || len(st.views) == 0 {
		return WorkspaceStrategySelection{}, nil, false
	}
	return st.selection, st.views[0], true
}

// forgetBranchViews drops the record of views a claim settlement already released.
func (m *Manager) forgetBranchViews(branchRoot string) {
	key := filepath.Clean(branchRoot)
	m.viewsMu.Lock()
	delete(m.views, key)
	m.viewsMu.Unlock()
	sharedBranchViews.Delete(key)
}

func (m *Manager) releaseBranchViews(branchRoot string) {
	m.viewsMu.Lock()
	key := filepath.Clean(branchRoot)
	st, ok := m.views[key]
	if ok {
		delete(m.views, key)
	}
	m.viewsMu.Unlock()
	sharedBranchViews.Delete(key)
	if !ok || st.selection.Adapter == nil {
		return
	}
	for _, v := range st.views {
		if v != nil {
			_, _ = st.selection.Adapter.Release(context.Background(), v.ViewID)
			if m.services != nil {
				_ = m.services.privateGit.ReleaseView(v.ViewID)
			}
		}
	}
}

// LookupBranchPreparedView returns the active PreparedView (when in-process) and its
// durable AdapterFacts and ViewID for a prepared worker branch root (#1658/#1665).
func LookupBranchPreparedView(branchRoot string) (*adapter.PreparedView, adapter.AdapterFacts, string, bool) {
	clean, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, adapter.AdapterFacts{}, "", false
	}
	if raw, ok := sharedBranchViews.Load(clean); ok {
		st := raw.(branchViewState)
		if len(st.views) > 0 && st.views[0] != nil {
			v := st.views[0]
			facts := v.Facts
			if facts.AdapterID == "" {
				facts = st.selection.Facts
			}
			return v, facts, v.ViewID, true
		}
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(clean))
	if err != nil || !meta.SnapshotComplete || meta.AdapterFacts == nil || meta.AdapterFacts.AdapterID == "" {
		return nil, adapter.AdapterFacts{}, "", false
	}
	viewID := strings.TrimSpace(meta.PreparedViewID)
	if viewID == "" {
		viewID = jobIDForBranch(clean)
	}
	return nil, *meta.AdapterFacts, viewID, true
}

// WithPreparationLimits configures bounded workspace preparation entry and byte limits on the Manager (#1658).
func (m *Manager) WithPreparationLimits(maxEntries, maxBytes int64) *Manager {
	if maxEntries > 0 {
		m.maxEntries = maxEntries
	}
	if maxBytes > 0 {
		m.maxBytes = maxBytes
	}
	return m
}

func effectiveLimits(ctx context.Context, m *Manager) (int64, int64) {
	var maxEntries, maxBytes int64
	if m != nil {
		maxEntries = m.maxEntries
		maxBytes = m.maxBytes
	}
	if maxEntries <= 0 {
		maxEntries = adapter.DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = adapter.DefaultMaxBytes
	}
	if override, ok := ctx.Value(workspaceLimitsKey{}).(workspaceLimitsContext); ok {
		if override.maxEntries > 0 {
			maxEntries = override.maxEntries
		}
		if override.maxBytes > 0 {
			maxBytes = override.maxBytes
		}
	}
	return maxEntries, maxBytes
}

func absDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("project dir required")
	}
	return filepath.Abs(dir)
}

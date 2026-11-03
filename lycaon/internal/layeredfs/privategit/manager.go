package privategit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// ManagerConfig configures a private worker Git Manager.
type ManagerConfig struct {
	BackingDir              string
	Store                   *RetainedObjectStore
	MaxCaptureRetries       int
	CaptureAttemptHook      func(worktreeDir string, attempt int)
	FailObjectImportHook    func(viewID, repoRel string) error
	FailObjectRetentionHook func(viewID, repoRel string) error
}

type managedView struct {
	viewID            string
	workerID          string
	rootID            string
	trunkDir          string
	worktreeDir       string
	materialized      bool
	core              *layeredfs.Core
	authorizedGitDirs []string

	mu           sync.Mutex
	accessOrigin AccessOrigin
	repos        map[string]*initializedRepo
}

// Manager coordinates lazy mounted repository initialization, materialized seed
// repository preparation, checkpoints, and trunk ref imports (#1671).
type Manager struct {
	cfg   ManagerConfig
	store *RetainedObjectStore

	mu              sync.RWMutex
	views           map[string]*managedView
	viewsByWorker   map[string]*managedView
	viewsByWorktree map[string]*managedView
	mountBindings   map[string]MountedViewBinding
	checkpoints     map[string]*CheckpointGitState
}

// NewManager creates a private Git Manager backed by an engine-retained object store.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.BackingDir == "" {
		return nil, fmt.Errorf("privategit: BackingDir is required")
	}
	absBacking, err := filepath.Abs(cfg.BackingDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absBacking, 0o755); err != nil {
		return nil, err
	}
	store := cfg.Store
	if store == nil {
		store, err = NewRetainedObjectStore(filepath.Join(absBacking, "retained-store"))
		if err != nil {
			return nil, err
		}
	}
	cfg.BackingDir = absBacking
	cfg.Store = store
	return &Manager{
		cfg:             cfg,
		store:           store,
		views:           make(map[string]*managedView),
		viewsByWorker:   make(map[string]*managedView),
		viewsByWorktree: make(map[string]*managedView),
		mountBindings:   make(map[string]MountedViewBinding),
		checkpoints:     make(map[string]*CheckpointGitState),
	}, nil
}

// Store returns the underlying engine-retained Git object store.
func (m *Manager) Store() *RetainedObjectStore {
	return m.store
}

// MountedClaim binds a mounted layeredfs.Core view without scanning for or initializing
// any repositories during workspace claim (#1671). A mounted worktree directory must be
// bound via MountedClaimWithWorktree or BindMountedWorktree before running Git operations.
func (m *Manager) MountedClaim(
	viewID, workerID, rootID, trunkDir string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if viewID == "" || trunkDir == "" || core == nil {
		return fmt.Errorf("privategit: viewID, trunkDir, and core are required")
	}
	absTrunk, err := filepath.Abs(trunkDir)
	if err != nil {
		return err
	}
	return registerMountedClaim(m, viewID, workerID, rootID, absTrunk, "", core, authorizedGitDirs...)
}

func registerManagedView(m *Manager, view *managedView) {
	m.mu.Lock()
	m.views[view.viewID] = view
	if view.workerID != "" {
		m.viewsByWorker[view.workerID] = view
	}
	if wt := canonicalCleanPath(view.worktreeDir); wt != "" {
		m.viewsByWorktree[wt] = view
	}
	m.mu.Unlock()
	registerActiveView(m, view)
}

// InterceptGitAccess implements layeredfs.GitInterceptor to initialize private Git
// metadata on first .git lookup or repository tool invocation (#1671).
func (m *Manager) InterceptGitAccess(
	viewID string,
	qpath layeredfs.QualifiedPath,
	writeIntent bool,
) (layeredfs.GitInterceptOutcome, error) {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return layeredfs.GitInterceptOutcome{}, layeredfs.ErrGitPending
	}
	repoRel, _, isGit := splitGitRelPath(qpath.Rel)
	if !isGit {
		return layeredfs.GitInterceptOutcome{}, nil
	}

	view.mu.Lock()
	origin := view.accessOrigin
	view.mu.Unlock()
	if origin == OriginNativeTool && writeIntent {
		return layeredfs.GitInterceptOutcome{}, ErrNativeToolGitMutationForbidden
	}
	if !view.materialized {
		if err := verifyActiveMountedView(m, view, "", ""); err != nil {
			return layeredfs.GitInterceptOutcome{}, fmt.Errorf("%w: %w", layeredfs.ErrGitPending, err)
		}
	}

	if err := ensureViewRepoInitialized(context.Background(), m, view, repoRel); err != nil {
		return layeredfs.GitInterceptOutcome{}, err
	}
	return layeredfs.GitInterceptOutcome{Intercepted: true}, nil
}

// SetAccessOrigin sets the active confinement origin for a view (filesystem vs native_tool).
func SetAccessOrigin(m *Manager, viewID string, origin AccessOrigin) error {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return err
	}
	view.mu.Lock()
	view.accessOrigin = origin
	view.mu.Unlock()
	return nil
}

// CheckNativeToolPath enforces the native file-tool restriction on direct .git mutations.
func CheckNativeToolPath(relPath string, writeIntent bool) error {
	if _, _, isGit := splitGitRelPath(relPath); isGit && writeIntent {
		return ErrNativeToolGitMutationForbidden
	}
	return nil
}

// RepoMetrics returns the initialization state and metrics for repoRel in viewID.
func (m *Manager) RepoMetrics(viewID, repoRel string) (InitMetrics, bool) {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return InitMetrics{State: StateUninitialized}, false
	}
	repoRel = cleanSlashRel(repoRel)
	view.mu.Lock()
	defer view.mu.Unlock()
	repo, ok := view.repos[repoRel]
	if !ok || repo == nil {
		return InitMetrics{State: StateUninitialized}, false
	}
	return repo.metrics, true
}

// ReleaseView releases active view leases from the retained store without invalidating
// any retained checkpoint leases (#1671).
func (m *Manager) ReleaseView(viewID string) error {
	m.mu.Lock()
	view, ok := m.views[viewID]
	if ok {
		delete(m.views, viewID)
		if view.workerID != "" {
			delete(m.viewsByWorker, view.workerID)
		}
		if wt := canonicalCleanPath(view.worktreeDir); wt != "" {
			delete(m.viewsByWorktree, wt)
		}
		delete(m.mountBindings, mountBindingKey(view.viewID, view.rootID))
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	unregisterActiveView(view)
	view.mu.Lock()
	var leases []string
	for _, r := range view.repos {
		if r.leaseID != "" {
			leases = append(leases, r.leaseID)
			r.leaseID = ""
		}
	}
	view.mu.Unlock()
	for _, lid := range leases {
		_ = m.store.ReleaseLease(lid)
	}
	return nil
}

func getManagedView(m *Manager, viewID string) (*managedView, error) {
	m.mu.RLock()
	view, ok := m.views[viewID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("privategit: unknown view %q", viewID)
	}
	return view, nil
}

func readyPrivateGitDir(m *Manager, viewID, repoRel string) (string, error) {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return "", layeredfs.ErrGitPending
	}
	repoRel = cleanSlashRel(repoRel)
	view.mu.Lock()
	defer view.mu.Unlock()
	repo, ok := view.repos[repoRel]
	if !ok || repo == nil || repo.privateGitDir == "" {
		return "", layeredfs.ErrGitPending
	}
	return repo.privateGitDir, nil
}

func forbiddenTargetDirs(m *Manager, excludeViewID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []string{m.store.RootDir(), filepath.Join(m.cfg.BackingDir, "views")}
	for id, v := range m.views {
		if id != excludeViewID {
			out = append(out, filepath.Join(m.cfg.BackingDir, "views", id), v.worktreeDir)
		}
	}
	return out
}

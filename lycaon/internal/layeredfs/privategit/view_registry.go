package privategit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

type activeViewEntry struct {
	mgr  *Manager
	view *managedView
}

var (
	activeViewsMu         sync.RWMutex
	activeViewsByID       = make(map[string]activeViewEntry)
	activeViewsByWorker   = make(map[string]activeViewEntry)
	activeViewsByWorktree = make(map[string]activeViewEntry)
)

func registerActiveView(mgr *Manager, view *managedView) {
	if mgr == nil || view == nil || view.viewID == "" {
		return
	}
	entry := activeViewEntry{mgr: mgr, view: view}
	activeViewsMu.Lock()
	activeViewsByID[view.viewID] = entry
	if view.workerID != "" {
		activeViewsByWorker[view.workerID] = entry
	}
	if wt := canonicalCleanPath(view.worktreeDir); wt != "" {
		activeViewsByWorktree[wt] = entry
	}
	activeViewsMu.Unlock()
}

func unregisterActiveView(view *managedView) {
	if view == nil {
		return
	}
	activeViewsMu.Lock()
	delete(activeViewsByID, view.viewID)
	if cur, ok := activeViewsByWorker[view.workerID]; ok && cur.view == view {
		delete(activeViewsByWorker, view.workerID)
	}
	if wt := canonicalCleanPath(view.worktreeDir); wt != "" {
		if cur, ok := activeViewsByWorktree[wt]; ok && cur.view == view {
			delete(activeViewsByWorktree, wt)
		}
	}
	activeViewsMu.Unlock()
}

func viewMatchesBoundary(view *managedView, cleanDir string) bool {
	if view == nil {
		return false
	}
	if cleanDir == "" {
		return true
	}
	return canonicalCleanPath(view.worktreeDir) == cleanDir || canonicalCleanPath(view.trunkDir) == cleanDir
}

func lookupActiveView(rootOrBranchDir, workerID string) (*Manager, *managedView, bool, error) {
	cleanDir := canonicalCleanPath(strings.TrimSpace(rootOrBranchDir))
	cleanWorker := strings.TrimSpace(workerID)

	activeViewsMu.RLock()
	defer activeViewsMu.RUnlock()

	if cleanWorker == "" {
		if cleanDir != "" {
			if _, isPrivateWT := activeViewsByWorktree[cleanDir]; isPrivateWT {
				return nil, nil, false, fmt.Errorf("%w: worker identity required for private worktree %q", ErrIdentityMismatch, rootOrBranchDir)
			}
		}
		return nil, nil, false, nil
	}

	entry, ok := activeViewsByID[cleanWorker]
	if !ok {
		entry, ok = activeViewsByWorker[cleanWorker]
	}
	if ok {
		if !viewMatchesBoundary(entry.view, cleanDir) {
			return nil, nil, false, fmt.Errorf("%w: worker %q is not bound to %q", ErrIdentityMismatch, cleanWorker, rootOrBranchDir)
		}
		return entry.mgr, entry.view, true, nil
	}

	if cleanDir != "" {
		if wtEntry, isPrivateWT := activeViewsByWorktree[cleanDir]; isPrivateWT {
			return nil, nil, false, fmt.Errorf("%w: worktree %q is bound to worker %q, not %q", ErrIdentityMismatch, rootOrBranchDir, wtEntry.view.workerID, cleanWorker)
		}
	}
	return nil, nil, false, nil
}

func lookupManagerView(mgr *Manager, rootOrBranchDir, workerID string) (*managedView, bool, error) {
	if mgr == nil {
		return nil, false, nil
	}
	cleanDir := canonicalCleanPath(strings.TrimSpace(rootOrBranchDir))
	cleanWorker := strings.TrimSpace(workerID)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if cleanWorker == "" {
		if cleanDir != "" {
			if _, isPrivateWT := mgr.viewsByWorktree[cleanDir]; isPrivateWT {
				return nil, false, fmt.Errorf("%w: worker identity required for private worktree %q", ErrIdentityMismatch, rootOrBranchDir)
			}
		}
		return nil, false, nil
	}

	v, ok := mgr.views[cleanWorker]
	if !ok {
		v, ok = mgr.viewsByWorker[cleanWorker]
	}
	if ok {
		if !viewMatchesBoundary(v, cleanDir) {
			return nil, false, fmt.Errorf("%w: worker %q is not bound to %q", ErrIdentityMismatch, cleanWorker, rootOrBranchDir)
		}
		return v, true, nil
	}
	if cleanDir != "" {
		if wtView, isPrivateWT := mgr.viewsByWorktree[cleanDir]; isPrivateWT {
			return nil, false, fmt.Errorf("%w: worktree %q is bound to worker %q, not %q", ErrIdentityMismatch, rootOrBranchDir, wtView.workerID, cleanWorker)
		}
	}
	return nil, false, nil
}

// ViewWorktreeDir returns the private worktree stage directory for viewID.
func (m *Manager) ViewWorktreeDir(viewID string) string {
	v, err := getManagedView(m, viewID)
	if err != nil {
		return ""
	}
	return v.worktreeDir
}

// PrepareViewGitOperation ensures private Git metadata is initialized on first use for an
// active worker view, synchronizes layeredfs.Core overlay changes into the private worktree
// stage before a Git operation, and returns a finish callback to sync worktree mutations back (#1671).
func PrepareViewGitOperation(
	ctx context.Context,
	rootOrBranchDir, workerID string,
) (worktreeDir string, finish func() error, managed bool, err error) {
	noop := func() error { return nil }
	mgr, view, ok, lErr := lookupActiveView(rootOrBranchDir, workerID)
	if lErr != nil {
		return "", noop, true, lErr
	}
	if !ok {
		return rootOrBranchDir, noop, false, nil
	}
	return prepareManagedViewOperation(ctx, mgr, view)
}

// PrepareManagerViewOperation prepares a view owned by mgr (or looked up via rootOrBranchDir/workerID).
func PrepareManagerViewOperation(
	ctx context.Context,
	mgr *Manager,
	rootOrBranchDir, workerID string,
) (worktreeDir string, finish func() error, managed bool, err error) {
	noop := func() error { return nil }
	if mgr != nil {
		matched, ok, lErr := lookupManagerView(mgr, rootOrBranchDir, workerID)
		if lErr != nil {
			return "", noop, true, lErr
		}
		if ok {
			return prepareManagedViewOperation(ctx, mgr, matched)
		}
	}
	return PrepareViewGitOperation(ctx, rootOrBranchDir, workerID)
}

func prepareManagedViewOperation(
	ctx context.Context,
	mgr *Manager,
	view *managedView,
) (string, func() error, bool, error) {
	noop := func() error { return nil }
	if _, err := mgr.InterceptGitAccess(view.viewID, layeredfs.QualifiedPath{
		RootID: view.rootID,
		Rel:    ".git",
	}, true); err != nil {
		return "", noop, true, err
	}
	if err := syncCoreOverlayToWorktree(view); err != nil {
		return "", noop, true, err
	}
	if err := materializeCandidateRefDiffs(ctx, view, "."); err != nil {
		return "", noop, true, err
	}
	before := captureWorktreeMutationBaseline(ctx, view, view.worktreeDir)
	finish := func() error {
		_ = retainNewPrivateObjects(ctx, mgr, view, ".")
		return syncWorktreeMutationsToCore(ctx, view, ".", before)
	}
	return view.worktreeDir, finish, true, nil
}

// MaterializeViewPaths ensures that explicit relative paths are materialized in a mounted
// view's sparse worktree stage and have --no-skip-worktree set prior to path-targeted Git commands (#1671).
func MaterializeViewPaths(ctx context.Context, rootOrBranchDir, workerID string, relPaths ...string) error {
	_, view, ok, err := lookupActiveView(rootOrBranchDir, workerID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return materializeMountedPaths(ctx, view, ".", relPaths)
}

// LookupExecutor returns a GitExecutor bound to an active view matching rootOrBranchDir and workerID.
func LookupExecutor(mgr *Manager, rootOrBranchDir, workerID string) (*GitExecutor, bool) {
	if mgr != nil {
		if matched, ok, err := lookupManagerView(mgr, rootOrBranchDir, workerID); err == nil && ok {
			return NewGitExecutor(mgr, matched.viewID, RemotePolicy{}), true
		} else if err != nil {
			return nil, false
		}
	}
	foundMgr, view, ok, err := lookupActiveView(rootOrBranchDir, workerID)
	if err != nil || !ok {
		return nil, false
	}
	return NewGitExecutor(foundMgr, view.viewID, RemotePolicy{}), true
}

// ViewPathExists reports whether relPath exists inside an active private-git worker view
// associated with projectOrWorktreeDir (#1671).
func ViewPathExists(projectOrWorktreeDir, relPath string) bool {
	rel := strings.TrimSpace(relPath)
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "-") {
		return false
	}
	cleanRel := filepath.ToSlash(filepath.Clean(rel))
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
		return false
	}
	cleanDir := canonicalCleanPath(strings.TrimSpace(projectOrWorktreeDir))
	if cleanDir == "" {
		return false
	}

	activeViewsMu.RLock()
	var views []*managedView
	for _, entry := range activeViewsByID {
		if viewMatchesBoundary(entry.view, cleanDir) {
			views = append(views, entry.view)
		}
	}
	activeViewsMu.RUnlock()

	for _, v := range views {
		if v.core != nil {
			if st, err := v.core.Stat(layeredfs.QualifiedPath{RootID: v.rootID, Rel: cleanRel}); err == nil && st.Kind != layeredfs.EntryAbsent {
				return true
			}
		}
		if _, err := os.Lstat(filepath.Join(v.worktreeDir, filepath.FromSlash(cleanRel))); err == nil {
			return true
		}
		if _, err := os.Lstat(filepath.Join(v.trunkDir, filepath.FromSlash(cleanRel))); err == nil {
			return true
		}
	}
	return false
}

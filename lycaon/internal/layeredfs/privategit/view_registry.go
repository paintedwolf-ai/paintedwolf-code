package privategit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/gitexec"
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
	return canonicalCleanPath(view.worktreeDir) == cleanDir || canonicalCleanPath(view.trunkDir) == cleanDir ||
		(view.branchDir != "" && canonicalCleanPath(view.branchDir) == cleanDir)
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
		if root, isRoot := activeViewsByWorktree[cleanDir]; isRoot && root.view.coreViewID == entry.view.viewID {
			return root.mgr, root.view, true, nil
		}
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
		if root, isRoot := mgr.viewsByWorktree[cleanDir]; isRoot && root.coreViewID == v.viewID {
			return root, true, nil
		}
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

// viewWorktreeDir returns the private worktree stage directory for viewID.
func viewWorktreeDir(m *Manager, viewID string) string {
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
	if !view.materialized {
		if err := verifyActiveMountedView(mgr, view, "", ""); err != nil {
			return "", noop, true, err
		}
	}
	if _, err := interceptViewAccess(mgr, view, layeredfs.QualifiedPath{
		RootID: view.rootID,
		Rel:    ".git",
	}, true); err != nil {
		return "", noop, true, err
	}
	if err := syncViewGitOverlay(view); err != nil {
		return "", noop, true, err
	}
	if !view.materialized && view.kernelMounted {
		if privGitDir, gErr := readyPrivateGitDir(mgr, view.viewID, "."); gErr == nil && privGitDir != "" {
			_, _, _ = gitexec.Run(ctx, filepath.Dir(privGitDir), []string{"-C", view.worktreeDir, "--git-dir=" + privGitDir, "--work-tree=" + view.worktreeDir, "update-index", "-q", "--refresh"}, gitexec.Opts{
				Profile: gitexec.ProfileHermetic,
			})
		}
	}
	if err := syncCoreOverlayToWorktree(view); err != nil {
		return "", noop, true, err
	}
	before := captureWorktreeMutationBaseline(view, view.worktreeDir)
	finish := func() error {
		overlayErr := syncViewGitOverlay(view)
		syncErr := syncWorktreeMutationsToCore(view, ".", before)
		retainErr := retainNewPrivateObjects(ctx, mgr, view, ".")
		return errors.Join(overlayErr, syncErr, retainErr)
	}
	return view.worktreeDir, finish, true, nil
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
			if !v.materialized {
				continue
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

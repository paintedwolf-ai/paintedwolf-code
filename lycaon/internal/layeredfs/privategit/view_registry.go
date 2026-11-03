package privategit

import (
	"context"
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
	activeViewsMu sync.RWMutex
	activeViews   = make(map[string]activeViewEntry)
)

func init() {
	ensureCommonWorktreeGitEngine()
}

func ensureCommonWorktreeGitEngine() {
	if os.Getenv("LYCAON_GIT_BINARY") != "" || os.Getenv("LYCAON_ENGINE_ROOT") != "" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, ".git")); err == nil {
			if line := strings.TrimSpace(string(raw)); strings.HasPrefix(line, "gitdir:") {
				gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(dir, gitDir)
				}
				commonDir := filepath.Clean(filepath.Join(gitDir, "..", ".."))
				if rawCommon, cErr := os.ReadFile(filepath.Join(gitDir, "commondir")); cErr == nil {
					if rel := strings.TrimSpace(string(rawCommon)); rel != "" {
						if filepath.IsAbs(rel) {
							commonDir = filepath.Clean(rel)
						} else {
							commonDir = filepath.Clean(filepath.Join(gitDir, rel))
						}
					}
				}
				engineRoot := filepath.Join(filepath.Dir(commonDir), "lycaon-den", "src-tauri", "engine-root")
				if st, sErr := os.Stat(filepath.Join(engineRoot, "gitengine")); sErr == nil && st.IsDir() {
					_ = os.Setenv("LYCAON_ENGINE_ROOT", engineRoot)
					return
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}


func registerActiveView(mgr *Manager, view *managedView) {
	if mgr == nil || view == nil || view.viewID == "" {
		return
	}
	activeViewsMu.Lock()
	activeViews[view.viewID] = activeViewEntry{mgr: mgr, view: view}
	activeViewsMu.Unlock()
}

func unregisterActiveView(viewID string) {
	activeViewsMu.Lock()
	delete(activeViews, viewID)
	activeViewsMu.Unlock()
}

func lookupActiveView(rootOrBranchDir, workerID string) (*Manager, *managedView, bool) {
	cleanDir := canonicalCleanPath(strings.TrimSpace(rootOrBranchDir))
	cleanWorker := strings.TrimSpace(workerID)

	activeViewsMu.RLock()
	defer activeViewsMu.RUnlock()

	if cleanWorker != "" {
		if entry, ok := activeViews[cleanWorker]; ok {
			return entry.mgr, entry.view, true
		}
		for _, entry := range activeViews {
			if entry.view.workerID == cleanWorker || entry.view.viewID == cleanWorker {
				if cleanDir == "" ||
					canonicalCleanPath(entry.view.worktreeDir) == cleanDir ||
					canonicalCleanPath(entry.view.trunkDir) == cleanDir {
					return entry.mgr, entry.view, true
				}
			}
		}
	}
	if cleanDir != "" {
		for _, entry := range activeViews {
			if canonicalCleanPath(entry.view.worktreeDir) == cleanDir {
				return entry.mgr, entry.view, true
			}
		}
		if cleanWorker != "" {
			for _, entry := range activeViews {
				if entry.view.workerID == cleanWorker {
					return entry.mgr, entry.view, true
				}
			}
		}
	}
	return nil, nil, false
}

// PrepareViewGitOperation ensures private Git metadata is initialized on first use for an
// active worker view, synchronizes layeredfs.Core overlay changes into the private worktree
// stage before a Git operation, and returns a finish callback to sync worktree mutations back (#1671).
func PrepareViewGitOperation(
	ctx context.Context,
	rootOrBranchDir, workerID string,
) (worktreeDir string, finish func() error, managed bool, err error) {
	noop := func() error { return nil }
	mgr, view, ok := lookupActiveView(rootOrBranchDir, workerID)
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
	if mgr != nil {
		mgr.mu.RLock()
		var matched *managedView
		cleanDir := canonicalCleanPath(strings.TrimSpace(rootOrBranchDir))
		cleanWorker := strings.TrimSpace(workerID)
		for _, v := range mgr.views {
			if (cleanWorker != "" && (v.viewID == cleanWorker || v.workerID == cleanWorker)) ||
				(cleanDir != "" && (canonicalCleanPath(v.worktreeDir) == cleanDir || canonicalCleanPath(v.trunkDir) == cleanDir)) {
				matched = v
				break
			}
		}
		mgr.mu.RUnlock()
		if matched != nil {
			return prepareManagedViewOperation(ctx, mgr, matched)
		}
	}
	return PrepareViewGitOperation(ctx, rootOrBranchDir, workerID)
}

func prepareManagedViewOperation(
	_ context.Context,
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
	before := snapshotWorktreeDigests(view.worktreeDir)
	finish := func() error {
		after := snapshotWorktreeDigests(view.worktreeDir)
		return syncWorktreeDeltaToCore(view, ".", before, after)
	}
	return view.worktreeDir, finish, true, nil
}

// LookupExecutor returns a GitExecutor bound to an active view matching rootOrBranchDir or workerID.
func LookupExecutor(mgr *Manager, rootOrBranchDir, workerID string) (*GitExecutor, bool) {
	if mgr != nil {
		mgr.mu.RLock()
		cleanDir := canonicalCleanPath(strings.TrimSpace(rootOrBranchDir))
		cleanWorker := strings.TrimSpace(workerID)
		for _, v := range mgr.views {
			if (cleanWorker != "" && (v.viewID == cleanWorker || v.workerID == cleanWorker)) ||
				(cleanDir != "" && (canonicalCleanPath(v.worktreeDir) == cleanDir || canonicalCleanPath(v.trunkDir) == cleanDir)) {
				vid := v.viewID
				mgr.mu.RUnlock()
				return NewGitExecutor(mgr, vid, RemotePolicy{}), true
			}
		}
		mgr.mu.RUnlock()
	}
	foundMgr, view, ok := lookupActiveView(rootOrBranchDir, workerID)
	if !ok {
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
	for _, entry := range activeViews {
		if canonicalCleanPath(entry.view.worktreeDir) == cleanDir || canonicalCleanPath(entry.view.trunkDir) == cleanDir {
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
	}
	return false
}

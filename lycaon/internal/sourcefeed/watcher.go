package sourcefeed

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/pkg/api"
)

// RootSpec is one attached project root covered by the shared watcher.
type RootSpec struct {
	ID          string
	WorkspaceID string
	Path        string
}

// DirectorySpec is one breadth-first catalog directory with its child count.
type DirectorySpec struct {
	Path       string
	EntryCount int
}

// RootSeed carries catalog directories for one bound root whose platform
// watcher registers directories one at a time.
type RootSeed struct {
	Path        string
	Directories []DirectorySpec
}

// ExternalBatch groups external changes from one watcher flush.
// Resync replaces incomplete path lists with a single invalidation.
type ExternalBatch struct {
	Changes   []Change
	Resync    bool
	HeadMoved bool
}

// ExternalObserver receives each external batch before it is published.
type ExternalObserver func(ctx context.Context, projectID string, batch ExternalBatch)

type projectWatch struct {
	projectID string
	roots     []RootSpec
	owner     *WatchOwner
	external  ExternalObserver
	changes   *changeConverger
	unbind    func()
}

type watchKey struct{ projectID, scopeID string }

var (
	watchRegMu sync.Mutex
	watchers   = map[watchKey]*projectWatch{}
)

// WatchNeedsSeed reports incomplete or per-directory watcher coverage.
func WatchNeedsSeed(rootPath string) bool {
	coverage := repochange.Coverage(rootPath)
	if !coverage.Watching {
		return false
	}
	return !coverage.Recursive || coverage.Truncated > 0
}

// SeedProjectWatch extends bound root streams with catalog directories.
func SeedProjectWatch(ctx context.Context, projectID string, seeds []RootSeed) {
	watchRegMu.Lock()
	var bound []RootSpec
	for key, watch := range watchers {
		if key.projectID == strings.TrimSpace(projectID) {
			bound = append(bound, watch.roots...)
		}
	}
	watchRegMu.Unlock()
	if len(bound) == 0 {
		return
	}
	for _, seed := range seeds {
		root, ok := rootForPath(bound, seed.Path)
		if !ok {
			continue
		}
		directories := cleanSeedDirectories(root.Path, seed.Directories)
		if len(directories) == 0 {
			continue
		}
		repochange.SeedWatch(ctx, root.Path, directories)
	}
}

// StopProjectWatch removes project routing.
func StopProjectWatch(ctx context.Context, projectID string) {
	watchRegMu.Lock()
	var removed []*projectWatch
	for key, watch := range watchers {
		if key.projectID == strings.TrimSpace(projectID) {
			removed = append(removed, watch)
			delete(watchers, key)
		}
	}
	var unused []string
	for _, watch := range removed {
		unused = append(unused, unusedWatchRootsLocked(watch)...)
	}
	watchRegMu.Unlock()
	for _, watch := range removed {
		watch.unbind()
		watch.changes.close(ctx)
	}
	for _, root := range unused {
		repochange.CloseRoot(root)
	}
}

func unusedWatchRootsLocked(removed *projectWatch) []string {
	if removed == nil {
		return nil
	}
	inUse := make(map[string]struct{})
	for _, watch := range watchers {
		for _, root := range watch.roots {
			inUse[filepath.Clean(root.Path)] = struct{}{}
		}
	}
	var unused []string
	for _, root := range removed.roots {
		path := filepath.Clean(root.Path)
		if _, ok := inUse[path]; !ok {
			unused = append(unused, path)
		}
	}
	return unused
}

// The index lock keeps file changes and ref movement in one pending batch.
func (w *projectWatch) gitBusy() bool {
	for _, root := range w.roots {
		repo, ok := gitrepo.Discover(root.Path)
		if !ok || repo.GitDir == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(repo.GitDir, "index.lock")); err == nil {
			return true
		}
	}
	return false
}

func (w *projectWatch) observe(ctx context.Context, event repochange.Event) {
	root, ok := w.rootFor(event.ProjectDir)
	if !ok {
		return
	}
	// Ref movement triggers reconciliation without adding file changes.
	if event.Kind == repochange.HeadMoved {
		w.changes.queueHeadMoved(ctx)
		return
	}
	if event.Kind == repochange.IndexChanged {
		w.changes.queueResync(ctx)
		return
	}
	if event.Kind != repochange.WorktreeChanged || event.Source != repochange.SourceWatcher {
		return
	}
	for _, rel := range event.Paths {
		rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
		if rel == "." {
			w.changes.queueResync(ctx)
			continue
		}
		if rel == "" || strings.HasPrefix(rel, "../") {
			continue
		}
		if slices.Contains(strings.Split(rel, "/"), ".git") {
			continue
		}
		w.changes.queue(ctx, root, rel)
	}
}

func (w *projectWatch) flushExternalChanges(ctx context.Context, pending pendingExternalBatch) {
	if w == nil || (len(pending.changes) == 0 && !pending.resync && !pending.headMoved) {
		return
	}
	watchRegMu.Lock()
	owner, external := w.owner, w.external
	watchRegMu.Unlock()
	if owner != nil {
		ownedCtx, finish, err := owner.work.Begin(ctx)
		if err != nil {
			return
		}
		defer finish()
		ctx = ownedCtx
	}
	batch := ExternalBatch{Resync: pending.resync, HeadMoved: pending.headMoved}
	// A window the watcher could not fully name publishes as one invalidation.
	if !pending.resync {
		batch.Changes = w.externalChanges(pending.changes)
	}
	if len(batch.Changes) == 0 && !batch.Resync && !batch.HeadMoved {
		return
	}
	// Head-only batches reconcile even when no working file changed.
	if external != nil {
		external(ctx, w.projectID, batch)
	}
	if ctx.Err() != nil {
		return
	}
	var err error
	if len(batch.Changes) == 0 {
		err = emitProjectSignal(ctx, w.projectID, w.roots[0].WorkspaceID, batch.Resync, batch.HeadMoved)
	} else {
		err = emitBatch(ctx, batch.Changes, false, batch.HeadMoved)
	}
	if err != nil {
		// Watcher delivery errors have no synchronous caller.
		slog.WarnContext(ctx, "source changes enqueue", "project_id", w.projectID, "count", len(batch.Changes), "err", err)
	}
}

// externalChanges drops the host's own recent writes and classifies the rest.
func (w *projectWatch) externalChanges(pending []pendingExternalChange) []Change {
	batch := make([]Change, 0, len(pending))
	for _, change := range pending {
		abs := filepath.Join(change.root.Path, filepath.FromSlash(change.rel))
		if isRecentHostWrite(abs) {
			continue
		}
		op := api.SourceChangeOpWrite
		var isDir *bool
		if info, err := os.Lstat(abs); os.IsNotExist(err) {
			op = api.SourceChangeOpDelete
		} else if err == nil {
			value := info.IsDir()
			isDir = &value
		}
		batch = append(batch, Change{
			ProjectID: w.projectID, WorkspaceID: change.root.WorkspaceID,
			WorkspaceKind: api.SourceWorkspaceKindProject,
			RootID:        change.root.ID, Path: change.rel, Op: op, IsDir: isDir,
			Origin: api.SourceChangeOriginExternal, AbsPath: abs,
		})
	}
	return batch
}

func (w *projectWatch) rootFor(rootPath string) (RootSpec, bool) {
	return rootForPath(w.roots, rootPath)
}

func rootForPath(roots []RootSpec, rootPath string) (RootSpec, bool) {
	rootPath = filepath.Clean(rootPath)
	for _, root := range roots {
		if filepath.Clean(root.Path) == rootPath {
			return root, true
		}
	}
	return RootSpec{}, false
}

func sameWatchRoots(a, b []RootSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cleanWatchRoots(roots []RootSpec) []RootSpec {
	out := make([]RootSpec, 0, len(roots))
	for _, root := range roots {
		id := strings.TrimSpace(root.ID)
		workspaceID := strings.TrimSpace(root.WorkspaceID)
		abs, err := filepath.Abs(strings.TrimSpace(root.Path))
		if id == "" || workspaceID == "" || err != nil {
			continue
		}
		if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
			continue
		}
		out = append(out, RootSpec{ID: id, WorkspaceID: workspaceID, Path: filepath.Clean(abs)})
	}
	return out
}

// cleanSeedDirectories keeps directories inside the root, the root itself
// first, each at most once.
func cleanSeedDirectories(rootPath string, directories []DirectorySpec) []repochange.WatchDirectory {
	out := make([]repochange.WatchDirectory, 0, len(directories)+1)
	out = append(out, repochange.WatchDirectory{Path: rootPath})
	seen := map[string]struct{}{rootPath: {}}
	for _, directory := range directories {
		path, pathErr := filepath.Abs(strings.TrimSpace(directory.Path))
		rel, relErr := filepath.Rel(rootPath, path)
		if pathErr != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			if path == rootPath {
				out[0].EntryCount = max(0, directory.EntryCount)
			}
			continue
		}
		seen[path] = struct{}{}
		out = append(out, repochange.WatchDirectory{Path: path, EntryCount: max(0, directory.EntryCount)})
	}
	return out
}

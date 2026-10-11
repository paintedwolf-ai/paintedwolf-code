package repochange

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/watchfd"
)

// WatcherRegistry tracks per-project-dir worktree watchers.
type WatcherRegistry struct {
	mu      sync.Mutex
	byDir   map[string]*worktreeWatcher
	maxDirs int
	// budget is process-wide: one descriptor pool for every worktree watcher.
	budget *watchfd.Budget
	// disabled keeps Ensure from starting platform watchers.
	disabled bool
}

// NewWatcherRegistry returns an empty registry.
func NewWatcherRegistry() *WatcherRegistry {
	return &WatcherRegistry{
		byDir:   map[string]*worktreeWatcher{},
		maxDirs: watchfd.MaxDirs,
		budget:  watchfd.NewProcessBudget(),
	}
}

var globalWatchers = NewWatcherRegistry()

// WatchDirectory is a catalog-discovered directory and its immediate width.
type WatchDirectory struct {
	Path       string
	EntryCount int
}

// EnsureRoot starts watching a root.
func EnsureRoot(ctx context.Context, root string) { globalWatchers.Ensure(ctx, root) }

// SeedWatch registers catalog directories without walking the tree again.
func SeedWatch(ctx context.Context, root string, directories []WatchDirectory) {
	globalWatchers.Seed(ctx, root, directories)
}

// CoverageRevalidationInterval bounds how stale an unwatched subtree may be.
// A consumer whose root coverage is incomplete trusts events for this long
// after its last walk, then walks again. Every disk-backed consumer of a root
// shares this one interval.
const CoverageRevalidationInterval = 15 * time.Minute

// WatchCoverage describes watcher completeness for one root.
// Epoch equality is reliable only when Complete is true.
type WatchCoverage struct {
	Root     string
	Watching bool
	Watched  int
	// Truncated counts unregistered directories.
	Truncated int
	// PolicyUnwatched counts lazy boundary roots, not unknown missing registrations.
	PolicyUnwatched int
	Complete        bool
	// Faulted means the platform stream reported an error; events since then
	// may be missing.
	Faulted bool
	// Recursive means one root registration observes every descendant, so
	// Watched counts registrations rather than covered directories.
	Recursive bool
}

// Coverage reports one root's watch registration state.
func Coverage(root string) WatchCoverage { return globalWatchers.Coverage(root) }

// Coverage reports one root's watch registration state.
func (r *WatcherRegistry) Coverage(root string) WatchCoverage {
	key := canonicalDir(root)
	out := WatchCoverage{Root: key}
	if r == nil || key == "" {
		return out
	}
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if w == nil {
		return out
	}
	return w.coverage()
}

// DirWatched reports whether one root-relative directory is watched.
func DirWatched(root, dir string) bool { return globalWatchers.DirWatched(root, dir) }

// DirWatched reports whether one root-relative directory holds a live watch
// registration.
func (r *WatcherRegistry) DirWatched(root, dir string) bool {
	if r == nil {
		return false
	}
	key := canonicalDir(root)
	if key == "" {
		return false
	}
	target := watchTarget(key, dir)
	if target == "" {
		return false
	}
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	return w.watchesDir(target)
}

// watchTarget resolves a root-relative directory to the absolute path a watcher
// registers it under, or "" when the directory escapes the root.
func watchTarget(root, dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return root
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(dir)))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	return target
}

// Ensure starts watching projectDir if not already.
func (r *WatcherRegistry) Ensure(ctx context.Context, projectDir string) {
	if r == nil || r.disabled {
		return
	}
	key, err := filepath.Abs(strings.TrimSpace(projectDir))
	if err != nil || key == "" {
		return
	}
	if info, err := os.Stat(key); err != nil || !info.IsDir() {
		return
	}
	r.mu.Lock()
	if _, ok := r.byDir[key]; ok {
		r.mu.Unlock()
		return
	}
	w, err := newWorktreeWatcher(ctx, key, r.maxDirs, r.budget)
	if err != nil {
		r.mu.Unlock()
		slog.WarnContext(ctx, "worktree watch could not start", "root", key, "err", err)
		return
	}
	r.byDir[key] = w
	r.mu.Unlock()
	coverage := w.coverage()
	slog.InfoContext(ctx, "worktree watch bound",
		"root", key, "recursive", coverage.Recursive, "complete", coverage.Complete)
	go w.loop(context.WithoutCancel(ctx))
}

// Seed extends an existing root watcher from deterministic catalog metadata.
// Each pass recomputes the root's truncation from what it could register, so
// coverage heals on a later pass once capacity frees.
func (r *WatcherRegistry) Seed(ctx context.Context, root string, directories []WatchDirectory) {
	if r == nil {
		return
	}
	key, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || key == "" {
		return
	}
	r.Ensure(ctx, key)
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if w == nil {
		return
	}
	policyUnwatched := 0
	filtered := make([]WatchDirectory, 0, len(directories))
	for _, directory := range directories {
		rel, err := filepath.Rel(key, directory.Path)
		if err == nil && w.scope.BoundaryPath(filepath.ToSlash(rel), true) != "" {
			if w.scope.BoundaryDir(filepath.ToSlash(rel)) != "" {
				policyUnwatched++
			}
			continue
		}
		filtered = append(filtered, directory)
	}
	w.mu.Lock()
	w.policyUnwatched = policyUnwatched
	w.mu.Unlock()
	eligible := watchOrder(key, filtered)
	// An empty catalog cannot resolve an existing coverage shortfall.
	if len(eligible) == 0 {
		return
	}
	missing := 0
	// The root is excluded from eligible; retry it with its live descriptor cost.
	if w.addMeasuredDir(key) != addRegistered {
		missing++
	}
	// A recursive root stream already observes every catalog directory; the
	// only registration that can fall short is the root itself.
	if w.recursive {
		w.setTruncated(missing)
		return
	}
	for i, directory := range eligible {
		outcome := w.addDir(directory.path, watchfd.Cost(directory.entryCount))
		if outcome == addFailed {
			missing++
		}
		if outcome == addExhausted {
			// Everything from here on is unwatched, not merely this directory.
			missing += len(eligible) - i
			break
		}
	}
	w.setTruncated(missing)
	coverage := w.coverage()
	slog.InfoContext(ctx, "worktree watch seeded",
		"root", key, "directories", len(eligible), "watched", coverage.Watched, "unwatched", coverage.Truncated)
}

// seedCandidate is one registrable catalog directory and its sort depth.
type seedCandidate struct {
	path       string
	entryCount int
	depth      int
}

// watchOrder prioritizes shallow directories when capacity is limited.
func watchOrder(root string, directories []WatchDirectory) []seedCandidate {
	out := make([]seedCandidate, 0, len(directories))
	for _, directory := range directories {
		path := filepath.Clean(directory.Path)
		if path == root {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		relSlash := filepath.ToSlash(rel)
		if watchfd.Cost(directory.EntryCount) > watchfd.DirCeiling() {
			continue
		}
		out = append(out, seedCandidate{
			path: path, entryCount: directory.EntryCount, depth: pathDepth(relSlash),
		})
	}
	slices.SortStableFunc(out, func(a, b seedCandidate) int {
		if order := cmp.Compare(a.depth, b.depth); order != 0 {
			return order
		}
		return cmp.Compare(a.path, b.path)
	})
	return out
}

// pathDepth sorts the root before its descendants.
func pathDepth(relSlash string) int {
	if relSlash == "." || relSlash == "" {
		return 0
	}
	return strings.Count(relSlash, "/") + 1
}

// CloseAll releases every watcher.
func (r *WatcherRegistry) CloseAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, w := range r.byDir {
		w.close()
		delete(r.byDir, k)
	}
}

// CloseRoot releases one root's watcher and descriptor budget.
func (r *WatcherRegistry) CloseRoot(root string) {
	if r == nil {
		return
	}
	key := canonicalDir(root)
	if key == "" {
		return
	}
	r.mu.Lock()
	w := r.byDir[key]
	delete(r.byDir, key)
	r.mu.Unlock()
	if w != nil {
		w.close()
	}
}

// ResetWatchersForTest closes the process-global registry.
func ResetWatchersForTest() {
	globalWatchers.CloseAll()
	globalWatchers = NewWatcherRegistry()
}

// DisableWatchersForTest closes the process-global registry and keeps it from
// starting platform watchers, so only events a test publishes reach observers.
func DisableWatchersForTest() {
	globalWatchers.CloseAll()
	globalWatchers = NewWatcherRegistry()
	globalWatchers.disabled = true
}

// CloseWatchers releases every process-wide filesystem descriptor at shutdown.
func CloseWatchers() { globalWatchers.CloseAll() }

// CloseRoot releases one process-wide root watcher.
func CloseRoot(root string) { globalWatchers.CloseRoot(root) }

// MarkCoverageCompleteForTest registers root as recursively watched with
// complete coverage and no platform stream, so a test can exercise the
// paths that trust the watcher by delivering events through Notify itself.
func MarkCoverageCompleteForTest(root string) {
	key := canonicalDir(root)
	if key == "" {
		return
	}
	w := &worktreeWatcher{
		root: key, recursive: true, done: make(chan struct{}),
		watched: map[string]int{key: 1},
	}
	globalWatchers.mu.Lock()
	if previous := globalWatchers.byDir[key]; previous != nil {
		previous.close()
	}
	globalWatchers.byDir[key] = w
	globalWatchers.mu.Unlock()
}

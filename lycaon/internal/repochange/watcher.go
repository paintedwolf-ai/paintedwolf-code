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

	"github.com/fsnotify/fsnotify"
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
	Complete  bool
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
	eligible := watchOrder(key, directories)
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

type worktreeWatcher struct {
	root         string
	physicalRoot string
	watcher      platformWatcher
	budget       *watchfd.Budget
	maxDirs      int
	// Recursive coverage comes from the root registration.
	recursive bool
	done      chan struct{}
	once      sync.Once

	// mu guards registration state.
	mu sync.Mutex
	// Registered costs prevent double-charging during reseeding.
	watched   map[string]int
	spent     int
	truncated int
	reported  bool
	faulted   bool
	// Repository metadata also emits ref and index signals.
	refDirs        map[string]struct{}
	refState       map[string]refFileState
	refNotifyTimer *time.Timer
	// closed fences callbacks already running.
	closed bool
}

func newWorktreeWatcher(ctx context.Context, root string, maxDirs int, budget *watchfd.Budget) (*worktreeWatcher, error) {
	fw, err := newPlatformWatcher(root)
	if err != nil {
		return nil, err
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		physicalRoot = root
	}
	w := &worktreeWatcher{
		physicalRoot: physicalRoot,
		root:         root,
		watcher:      fw,
		budget:       budget,
		maxDirs:      maxDirs,
		recursive:    watchfd.Recursive,
		done:         make(chan struct{}),
		watched:      map[string]int{},
	}
	if w.addMeasuredDir(root) != addRegistered {
		w.setTruncated(1)
	}
	w.armRefWatch()
	return w, nil
}

// Live entry counts bound descriptor cost before a catalog is available.
func (w *worktreeWatcher) addMeasuredDir(path string) addOutcome {
	if !watchfd.EntryDescriptors {
		return w.addDir(path, watchfd.Cost(0))
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return addFailed
	}
	cost := watchfd.Cost(len(entries))
	if cost > watchfd.DirCeiling() {
		return addExhausted
	}
	return w.addDir(path, cost)
}

// addOutcome distinguishes a rejected directory from exhausted capacity.
type addOutcome int

const (
	addRegistered addOutcome = iota
	// addFailed allows sibling registration to continue.
	addFailed
	// addExhausted stops registration.
	addExhausted
)

// addDir registers one directory against the shared budget. An already
// registered directory is a free no-op, so re-seeding stays idempotent. Under
// a recursive stream the registration carries no cost and no cap: the root
// already observes the directory, and registering it only names a git ref
// surface or a tree the stream must extend to.
func (w *worktreeWatcher) addDir(path string, cost int) addOutcome {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.watched[path]; ok {
		return addRegistered
	}
	if w.recursive {
		cost = 0
	} else if len(w.watched) >= w.maxDirs || !w.budget.Take(cost) {
		return addExhausted
	}
	if err := w.watcher.Add(path); err != nil {
		w.budget.Give(cost)
		return addFailed
	}
	w.watched[path] = cost
	w.spent += cost
	return addRegistered
}

// repriceDir keeps the budget aligned with kqueue as entries appear and
// disappear. If a directory becomes too wide or the process budget is spent,
// its watch is dropped and the polling path becomes authoritative.
func (w *worktreeWatcher) repriceDir(path string, cost int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A recursive stream prices nothing: the directory stays covered whether or
	// not it was ever registered, and however wide it grows.
	if w.recursive {
		return true
	}
	previous, ok := w.watched[path]
	if !ok {
		return false
	}
	delta := cost - previous
	if cost > watchfd.DirCeiling() || (delta > 0 && !w.budget.Take(delta)) {
		_ = w.watcher.Remove(path)
		delete(w.watched, path)
		delete(w.refDirs, path)
		w.spent -= previous
		w.budget.Give(previous)
		return false
	}
	if delta < 0 {
		w.budget.Give(-delta)
	}
	w.watched[path] = cost
	w.spent += delta
	return true
}

// forgetRemovedTree returns accounting for watches the kernel discarded when
// a directory was removed or renamed out of the tree.
func (w *worktreeWatcher) forgetRemovedTree(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	prefix := path + string(os.PathSeparator)
	for watched, cost := range w.watched {
		if watched != path && !strings.HasPrefix(watched, prefix) {
			continue
		}
		_ = w.watcher.Remove(watched)
		delete(w.watched, watched)
		delete(w.refDirs, watched)
		w.spent -= cost
		w.budget.Give(cost)
	}
}

// Zero truncation clears the shortfall and re-arms its warning.
func (w *worktreeWatcher) setTruncated(missing int) {
	w.mu.Lock()
	w.truncated = missing
	if missing == 0 {
		w.reported = false
	}
	w.mu.Unlock()
	w.reportCoverage()
}

// noteUnwatchedDir counts one newly discovered directory that could not be
// registered; the next seed pass recomputes the exact shortfall.
func (w *worktreeWatcher) noteUnwatchedDir() {
	w.mu.Lock()
	w.truncated++
	w.mu.Unlock()
	w.reportCoverage()
}

func (w *worktreeWatcher) coverage() WatchCoverage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.coverageLocked()
}

// watchesDir reports whether writes in one absolute directory reach the epoch.
// A recursive root stream covers every directory beneath it except the ones
// whose events the loop drops; elsewhere only a live registration counts.
func (w *worktreeWatcher) watchesDir(path string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.recursive {
		if _, ok := w.watched[w.root]; !ok {
			return false
		}
		return w.coveredByRootLocked(path)
	}
	_, ok := w.watched[path]
	return ok
}

// coveredByRootLocked reports whether path lies under the recursive stream.
func (w *worktreeWatcher) coveredByRootLocked(path string) bool {
	if path == w.root {
		return true
	}
	rel, err := filepath.Rel(w.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func (w *worktreeWatcher) coverageLocked() WatchCoverage {
	return WatchCoverage{
		Root: w.root, Watching: true, Watched: len(w.watched),
		Truncated: w.truncated, Complete: w.truncated == 0 && !w.faulted,
		Recursive: w.recursive, Faulted: w.faulted,
	}
}

// reportCoverage logs the first coverage shortfall for a root.
func (w *worktreeWatcher) reportCoverage() {
	w.mu.Lock()
	warn := w.truncated > 0 && !w.reported
	if warn {
		w.reported = true
	}
	current := w.coverageLocked()
	w.mu.Unlock()
	if warn {
		slog.Warn("worktree watch is truncated; the epoch cannot see every write",
			"root", w.root, "watched_dirs", current.Watched, "unwatched_dirs", current.Truncated)
	}
}

func (w *worktreeWatcher) loop(ctx context.Context) {
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.watcher.EventChannel():
			if !ok {
				return
			}
			// Permission changes can make previously unreadable paths discoverable.
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			if ev.Name == w.root && ev.Op == fsnotify.Write {
				continue
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.forgetRemovedTree(ev.Name)
			}
			parent := filepath.Dir(ev.Name)
			if watchfd.EntryDescriptors && ev.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 && w.watchesDir(parent) {
				entries, err := os.ReadDir(parent)
				if err == nil {
					if !w.repriceDir(parent, watchfd.Cost(len(entries))) {
						w.noteUnwatchedDir()
					}
				}
			}
			if w.isRefEvent(ev.Name) {
				w.handleRefEvent(ctx, ev)
			}
			rel, err := filepath.Rel(w.root, ev.Name)
			if err == nil && (rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))) && w.physicalRoot != "" {
				// Ref streams may report the physical alias of the logical root.
				rel, err = filepath.Rel(w.physicalRoot, ev.Name)
			}
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			info, statErr := os.Stat(ev.Name)
			isDir := statErr == nil && info.IsDir()
			if ev.Op&fsnotify.Create != 0 && isDir && relSlash == ".git" {
				// A repository appeared under a watched root; start watching
				// its ref surfaces.
				w.armRefWatch()
			}
			if ev.Op&fsnotify.Create != 0 && isDir && !w.recursive {
				if w.addMeasuredDir(ev.Name) != addRegistered {
					w.noteUnwatchedDir()
				}
			}
			NotifyWorktreeChangesDebounced(ctx, w.root, []WorktreeChange{{Path: relSlash, Kind: classifyWatcherChange(ev.Op, isDir, statErr)}}, SourceWatcher)
		case err, ok := <-w.watcher.ErrorChannel():
			if !ok {
				return
			}
			w.mu.Lock()
			w.faulted = true
			w.mu.Unlock()
			slog.WarnContext(ctx, "worktree watcher coverage interrupted", "root", w.root, "error", err)
			NotifyWorktreeChangesDebounced(ctx, w.root, []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, SourceWatcher)
		case _, ok := <-w.watcher.ResyncChannel():
			if !ok {
				return
			}
			NotifyWorktreeChangesDebounced(ctx, w.root, []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, SourceWatcher)
		}
	}
}

func classifyWatcherChange(op fsnotify.Op, isDir bool, statErr error) WorktreeChangeKind {
	if op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) != 0 {
		return WorktreeChangeStructural
	}
	if op&fsnotify.Write != 0 {
		if statErr != nil {
			return WorktreeChangeUnknown
		}
		if isDir {
			return WorktreeChangeStructural
		}
		return WorktreeChangeContent
	}
	return WorktreeChangeUnknown
}

func (w *worktreeWatcher) close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.stopRefNotify()
		if w.watcher != nil {
			_ = w.watcher.Close()
		}
		close(w.done)
		w.mu.Lock()
		spent := w.spent
		w.spent = 0
		w.watched = map[string]int{}
		w.mu.Unlock()
		if w.budget != nil {
			w.budget.Give(spent)
		}
	})
}

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

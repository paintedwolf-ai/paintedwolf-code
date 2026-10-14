package repochange

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/watchfd"
)

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
	watched         map[string]int
	spent           int
	truncated       int
	policyUnwatched int
	scope           *sourcescope.Scope
	reported        bool
	faulted         bool
	// overflowed holds between a dropped event window and the resync that
	// closes it; overflows counts the windows for diagnostics.
	overflowed bool
	overflows  int
	// watchLimit records that the kernel refused the last registration for
	// lack of per-user watch capacity, until a later registration succeeds.
	watchLimit    bool
	limitReported bool
	refs          refObserver
}

func newWorktreeWatcher(ctx context.Context, root string, maxDirs int, budget *watchfd.Budget) (*worktreeWatcher, error) {
	cfg, err := sourcescope.DefaultConfig()
	if err != nil {
		return nil, err
	}
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
		scope:        sourcescope.New(root, sourcescope.Options{Plane: cfg.Catalog}),
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
	w.refs.arm(w.root, w.addMeasuredDir)
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
		w.noteRegistrationFailureLocked(path, err)
		return addFailed
	}
	w.watchLimit = false
	w.watched[path] = cost
	w.spent += cost
	return addRegistered
}

// ENOSPC from a watch registration is the per-user watch limit, not disk
// space; the shortfall heals only when the limit is raised or watches free up.
func (w *worktreeWatcher) noteRegistrationFailureLocked(path string, err error) {
	if !errors.Is(err, syscall.ENOSPC) {
		return
	}
	w.watchLimit = true
	if w.limitReported {
		return
	}
	w.limitReported = true
	slog.Warn("worktree watch registration refused: the per-user watch limit is reached; outside edits in unwatched directories surface on the next inventory pass",
		"root", w.root, "directory", path, "watched_dirs", len(w.watched), "recovery", "raise fs.inotify.max_user_watches")
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
		w.refs.forget(path)
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
		w.refs.forget(watched)
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
	if w.scope != nil {
		rel, err := filepath.Rel(w.root, path)
		if err == nil && w.scope.BoundaryPath(filepath.ToSlash(rel), true) != "" {
			return false
		}
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
		Truncated: w.truncated, PolicyUnwatched: w.policyUnwatched, Complete: w.truncated == 0 && !w.faulted && !w.overflowed,
		Recursive: w.recursive, Faulted: w.faulted, Overflows: w.overflows, WatchLimit: w.watchLimit,
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
			if w.refs.contains(ev.Name) {
				w.refs.observe(ctx, w.root, ev, w.addMeasuredDir)
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
			if filepath.Base(relSlash) == ".git" && filepath.Dir(relSlash) != "." && ev.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				checkout := filepath.ToSlash(filepath.Dir(relSlash))
				w.forgetRemovedTree(filepath.Join(w.root, filepath.FromSlash(checkout)))
				NotifyWorktreeChangesDebounced(ctx, w.root, []WorktreeChange{{Path: checkout, Kind: WorktreeChangeStructural}}, SourceWatcher)
				continue
			}
			if w.scope.BoundaryPath(relSlash, false) != "" {
				continue
			}
			// A symlink is an entry of its parent; the tree it points at is
			// outside the watched geometry, as it is for the catalog.
			info, statErr := os.Lstat(ev.Name)
			isDir := statErr == nil && info.IsDir()
			if ev.Op&fsnotify.Create != 0 && isDir && relSlash == ".git" {
				// A repository appeared under a watched root; start watching
				// its ref surfaces.
				w.refs.arm(w.root, w.addMeasuredDir)
			}
			if ev.Op&fsnotify.Create != 0 && isDir && !w.recursive && w.scope.BoundaryDir(relSlash) == "" {
				if w.addMeasuredDir(ev.Name) != addRegistered {
					w.noteUnwatchedDir()
				}
			}
			NotifyWorktreeChangesDebounced(ctx, w.root, []WorktreeChange{{Path: relSlash, Kind: classifyWatcherChange(ev.Op, isDir, statErr)}}, SourceWatcher)
		case err, ok := <-w.watcher.ErrorChannel():
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.recoverOverflow(ctx)
				continue
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

// recoverOverflow closes a dropped-event window. The stream itself stays
// valid after the kernel reports an overflow, so one synchronous resync
// advances the epoch past the lost events and every observer rescans; the
// window counts as incomplete only until that delivery returns.
func (w *worktreeWatcher) recoverOverflow(ctx context.Context) {
	w.mu.Lock()
	w.overflowed = true
	w.overflows++
	count := w.overflows
	w.mu.Unlock()
	slog.WarnContext(ctx, "worktree watcher event queue overflowed; rescanning the root", "root", w.root, "overflows", count)
	Notify(ctx, Event{ProjectDir: w.root, Kind: WorktreeChanged, Changes: []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, Source: SourceWatcher})
	w.mu.Lock()
	w.overflowed = false
	w.mu.Unlock()
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
		w.refs.close()
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

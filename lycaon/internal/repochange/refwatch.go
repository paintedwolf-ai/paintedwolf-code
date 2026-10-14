package repochange

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// Coalesces ref, lock, and reflog writes into one HeadMoved event.
const refNotifyDebounce = 200 * time.Millisecond

type refFileState struct {
	digest [sha256.Size]byte
	exists bool
}

// refObserver tracks repository metadata independently of watch coverage.
type refObserver struct {
	// root names every registration; physicalRoot is the resolved alias that
	// repository discovery reports.
	root         string
	physicalRoot string

	mu     sync.Mutex
	dirs   map[string]struct{}
	states map[string]refFileState
	timer  *time.Timer
	closed bool
}

// Roots without a repository re-arm when a .git entry appears.
func (w *refObserver) arm(register func(string) addOutcome) {
	repo, ok := w.discover()
	if !ok || repo.GitDir == "" || repo.CommonDir == "" {
		return
	}
	dirs := make([]string, 0, 4)
	if repo.CommonDir != repo.GitDir {
		// Parent-first registration lets recursive backends share one stream.
		dirs = append(dirs, repo.CommonDir)
	}
	dirs = append(dirs,
		repo.GitDir,
		filepath.Join(repo.GitDir, "logs"),
		filepath.Join(repo.CommonDir, "refs", "heads"),
	)
	w.mu.Lock()
	if w.dirs == nil {
		w.dirs = map[string]struct{}{}
	}
	if w.states == nil {
		w.states = map[string]refFileState{}
	}
	w.mu.Unlock()
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if register(dir) == addRegistered {
			w.mu.Lock()
			w.dirs[dir] = struct{}{}
			w.mu.Unlock()
		}
	}
	w.seedRefState(repo)
}

// discover reports the repository in the root's namespace, so ref
// registrations carry the names the root's own events use.
func (w *refObserver) discover() (gitrepo.Repo, bool) {
	repo, ok := gitrepo.Discover(w.root)
	if !ok {
		return repo, false
	}
	repo.Root = w.logical(repo.Root)
	repo.GitDir = w.logical(repo.GitDir)
	repo.CommonDir = w.logical(repo.CommonDir)
	return repo, true
}

// Paths outside the physical root, such as a linked worktree's common
// directory, have no logical alias and keep their resolved form.
func (w *refObserver) logical(path string) string {
	if path == "" {
		return path
	}
	rel, err := filepath.Rel(w.physicalRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return path
	}
	return filepath.Join(w.root, rel)
}

func (w *refObserver) contains(name string) bool {
	dir := filepath.Dir(name)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.dirs[name]; ok {
		return true
	}
	if _, ok := w.dirs[dir]; ok {
		return true
	}
	// Parent coverage includes branch directories awaiting registration.
	for watched := range w.dirs {
		if strings.HasSuffix(watched, string(os.PathSeparator)+filepath.Join("refs", "heads")) &&
			strings.HasPrefix(dir, watched+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// Index changes and committed-history movement emit distinct events.
func (w *refObserver) observe(ctx context.Context, ev fsnotify.Event, register func(string) addOutcome) {
	if filepath.Base(ev.Name) == "index" {
		if repo, ok := w.discover(); ok && ev.Name == filepath.Join(repo.GitDir, "index") {
			if w.refChanged(ev.Name) {
				Notify(ctx, Event{ProjectDir: w.root, Kind: IndexChanged, Source: SourceWatcher})
			}
			return
		}
	}
	base := strings.TrimSuffix(filepath.Base(ev.Name), ".lock")
	dir := filepath.Dir(ev.Name)
	movement := base == "HEAD" || base == "packed-refs" ||
		strings.Contains(dir, string(os.PathSeparator)+"logs") ||
		strings.Contains(dir, filepath.Join("refs", "heads"))
	if !movement {
		return
	}
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			// A new refs/heads subdirectory (slashed branch name).
			if register(ev.Name) == addRegistered {
				w.mu.Lock()
				w.dirs[ev.Name] = struct{}{}
				w.mu.Unlock()
			}
			return
		}
	}
	if !w.refChanged(ev.Name) {
		return
	}
	w.schedule(ctx)
}

func (w *refObserver) seedRefState(repo gitrepo.Repo) {
	for _, file := range []string{
		filepath.Join(repo.GitDir, "index"),
		filepath.Join(repo.GitDir, "HEAD"),
		filepath.Join(repo.GitDir, "packed-refs"),
	} {
		w.rememberRefState(file)
	}
	for _, root := range []string{
		filepath.Join(repo.GitDir, "logs"),
		filepath.Join(repo.CommonDir, "refs", "heads"),
	} {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				w.rememberRefState(path)
			}
			return nil
		})
	}
}

func (w *refObserver) rememberRefState(path string) {
	state := readRefFileState(path)
	w.mu.Lock()
	w.states[path] = state
	w.mu.Unlock()
}

func (w *refObserver) refChanged(path string) bool {
	current := readRefFileState(path)
	w.mu.Lock()
	previous, known := w.states[path]
	w.states[path] = current
	w.mu.Unlock()
	if !known {
		return current.exists
	}
	return previous != current
}

func readRefFileState(path string) refFileState {
	raw, err := os.ReadFile(path)
	if err != nil {
		return refFileState{}
	}
	return refFileState{digest: sha256.Sum256(raw), exists: true}
}

func (w *refObserver) schedule(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.timer != nil {
		w.timer.Reset(refNotifyDebounce)
		return
	}
	w.timer = time.AfterFunc(refNotifyDebounce, func() {
		w.mu.Lock()
		w.timer = nil
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return
		}
		Notify(ctx, Event{ProjectDir: w.root, Kind: HeadMoved, Source: SourceWatcher})
	})
}

func (w *refObserver) close() {
	w.mu.Lock()
	timer := w.timer
	w.timer = nil
	w.closed = true
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (w *refObserver) forget(path string) {
	w.mu.Lock()
	delete(w.dirs, path)
	w.mu.Unlock()
}

package repochange

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// Roots without a repository re-arm when a .git entry appears.
func (w *worktreeWatcher) armRefWatch() {
	repo, ok := gitrepo.Discover(w.root)
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
	if w.refDirs == nil {
		w.refDirs = map[string]struct{}{}
	}
	if w.refState == nil {
		w.refState = map[string]refFileState{}
	}
	w.mu.Unlock()
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if w.addMeasuredDir(dir) == addRegistered {
			w.mu.Lock()
			w.refDirs[dir] = struct{}{}
			w.mu.Unlock()
		}
	}
	w.seedRefState(repo)
}

// isRefEvent reports whether an event path lives on a watched ref surface.
func (w *worktreeWatcher) isRefEvent(name string) bool {
	dir := filepath.Dir(name)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.refDirs[name]; ok {
		return true
	}
	if _, ok := w.refDirs[dir]; ok {
		return true
	}
	// Parent coverage includes branch directories awaiting registration.
	for watched := range w.refDirs {
		if strings.HasSuffix(watched, string(os.PathSeparator)+filepath.Join("refs", "heads")) &&
			strings.HasPrefix(dir, watched+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// handleRefEvent distinguishes index changes from committed-history movement.
func (w *worktreeWatcher) handleRefEvent(ctx context.Context, ev fsnotify.Event) {
	if filepath.Base(ev.Name) == "index" {
		if repo, ok := gitrepo.Discover(w.root); ok && ev.Name == filepath.Join(repo.GitDir, "index") {
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
			if w.addMeasuredDir(ev.Name) == addRegistered {
				w.mu.Lock()
				w.refDirs[ev.Name] = struct{}{}
				w.mu.Unlock()
			}
			return
		}
	}
	if !w.refChanged(ev.Name) {
		return
	}
	w.scheduleHeadNotify(ctx)
}

func (w *worktreeWatcher) seedRefState(repo gitrepo.Repo) {
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

func (w *worktreeWatcher) rememberRefState(path string) {
	state := readRefFileState(path)
	w.mu.Lock()
	w.refState[path] = state
	w.mu.Unlock()
}

func (w *worktreeWatcher) refChanged(path string) bool {
	current := readRefFileState(path)
	w.mu.Lock()
	previous, known := w.refState[path]
	w.refState[path] = current
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

// scheduleHeadNotify emits one debounced HeadMoved unless closed.
func (w *worktreeWatcher) scheduleHeadNotify(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.refNotifyTimer != nil {
		w.refNotifyTimer.Reset(refNotifyDebounce)
		return
	}
	root := w.root
	w.refNotifyTimer = time.AfterFunc(refNotifyDebounce, func() {
		w.mu.Lock()
		w.refNotifyTimer = nil
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return
		}
		Notify(ctx, Event{ProjectDir: root, Kind: HeadMoved, Source: SourceWatcher})
	})
}

// stopRefNotify cancels a pending debounced notification at close.
func (w *worktreeWatcher) stopRefNotify() {
	w.mu.Lock()
	timer := w.refNotifyTimer
	w.refNotifyTimer = nil
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

package repochange

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// maxAdoptedChanges bounds total inspected entries when a new directory tree
// appears on a per-directory watcher (Linux inotify / kqueue). Past this cap,
// the watcher stops reading and emits a bare resync.
const (
	maxAdoptedChanges = 512
	adoptReadChunk    = 64
)

func (w *worktreeWatcher) isWatched(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.watched[path]
	return ok
}

// adoptCreatedTree registers watches on a newly created directory and its
// already-present descendants through a root-confined handle, reading in
// bounded chunks and charging every inspected entry (including excluded names)
// against maxAdoptedChanges so wide directories never trigger unbounded I/O.
func (w *worktreeWatcher) adoptCreatedTree(absDir, relDir string) []WorktreeChange {
	changes, _ := w.adoptCreatedTreeMeasured(absDir, relDir)
	return changes
}

func (w *worktreeWatcher) adoptCreatedTreeMeasured(absDir, relDir string) ([]WorktreeChange, int) {
	rootHandle, err := os.OpenRoot(w.root)
	if err != nil {
		return []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, 0
	}
	defer func() { _ = rootHandle.Close() }()

	type dirItem struct{ abs, rel string }
	queue := []dirItem{{abs: absDir, rel: relDir}}
	changes := []WorktreeChange{{Path: relDir, Kind: WorktreeChangeStructural}}
	inspected := 0
	needsResync := false

	for len(queue) > 0 && !needsResync {
		cur := queue[0]
		queue = queue[1:]

		if w.addMeasuredDir(cur.abs) != addRegistered {
			w.noteUnwatchedDir()
			needsResync = true
			break
		}
		// Open through rootHandle strictly after inotify_add_watch so path
		// traversal stays root-confined and concurrent writes either appear
		// here or trigger a subsequent kernel event.
		dirFile, openErr := rootHandle.Open(filepath.FromSlash(cur.rel))
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			needsResync = true
			break
		}
		for !needsResync {
			entries, readErr := dirFile.ReadDir(adoptReadChunk)
			for _, entry := range entries {
				inspected++
				if inspected > maxAdoptedChanges {
					needsResync = true
					break
				}
				name := entry.Name()
				childAbs := filepath.Join(cur.abs, name)
				if IsPrivatePath(childAbs) {
					continue
				}
				childRel := cur.rel + "/" + name
				if w.scope != nil && w.scope.BoundaryPath(childRel, false) != "" {
					continue
				}
				changes = append(changes, WorktreeChange{Path: childRel, Kind: WorktreeChangeStructural})
				// DirEntry.IsDir is false for symlinks to directories, keeping
				// symlinked subtrees outside the watched geometry.
				if entry.IsDir() && (w.scope == nil || w.scope.BoundaryDir(childRel) == "") {
					queue = append(queue, dirItem{abs: childAbs, rel: childRel})
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				needsResync = true
				break
			}
		}
		_ = dirFile.Close()
	}

	if needsResync {
		return []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}, inspected
	}
	return changes, inspected
}

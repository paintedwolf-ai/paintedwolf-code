package repochange

import (
	"errors"
	"os"
	"path/filepath"
)

// maxAdoptedChanges bounds inline child enumeration when a new directory tree
// appears on a per-directory watcher (Linux inotify / kqueue). Past this cap,
// the watcher emits a bare resync so observers rescan without stalling the loop.
const maxAdoptedChanges = 512

func (w *worktreeWatcher) isWatched(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.watched[path]
	return ok
}

// adoptCreatedTree registers watches on a newly created directory and its
// already-present descendants, returning changes for every discovered child so
// files written before inotify_add_watch completes are never silently omitted.
// When watch budget, errors, or the adoption bound prevent complete coverage,
// it emits a bare WorktreeChangeResync instead of an incomplete path list.
func (w *worktreeWatcher) adoptCreatedTree(absDir, relDir string) []WorktreeChange {
	type dirItem struct{ abs, rel string }
	queue := []dirItem{{abs: absDir, rel: relDir}}
	changes := []WorktreeChange{{Path: relDir, Kind: WorktreeChangeStructural}}
	needsResync := false

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if w.addMeasuredDir(cur.abs) != addRegistered {
			w.noteUnwatchedDir()
			needsResync = true
			break
		}
		// ReadDir runs strictly after inotify_add_watch so any child created
		// concurrently either appears here or produces a subsequent kernel event.
		entries, err := os.ReadDir(cur.abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			needsResync = true
			break
		}
		for _, entry := range entries {
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
			if len(changes) > maxAdoptedChanges {
				needsResync = true
				break
			}
			// DirEntry.IsDir is false for symlinks to directories, keeping
			// symlinked subtrees outside the watched geometry.
			if entry.IsDir() && (w.scope == nil || w.scope.BoundaryDir(childRel) == "") {
				queue = append(queue, dirItem{abs: childAbs, rel: childRel})
			}
		}
		if needsResync {
			break
		}
	}

	if needsResync {
		return []WorktreeChange{{Path: ".", Kind: WorktreeChangeResync}}
	}
	return changes
}

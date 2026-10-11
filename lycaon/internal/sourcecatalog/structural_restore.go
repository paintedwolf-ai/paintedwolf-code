package sourcecatalog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

// Matching directory stamps permit reuse; concurrent invalidations take precedence.
func (s *indexStore) restoreStructure(ctx context.Context) (bool, []string) {
	s.mu.Lock()
	s.initializeStructureLocked()
	empty := s.structure.directories.Len() == 0
	s.mu.Unlock()
	// A change between the check and the watch registration would go unseen.
	if !empty || !repochange.DirWatched(s.root.Path, ".") {
		return false, nil
	}
	started := time.Now()
	generation, err := loadStructuralCheckpoint(ctx, s, true)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && ctx.Err() == nil {
			slog.DebugContext(ctx, "Rebuilding structural cache", "root", s.root.ID, "error", err)
		}
		return false, nil
	}
	retained := false
	defer func() {
		if !retained {
			generation.close()
		}
	}()
	root, found, err := generation.directories.Get(ctx, ".")
	if err != nil || !found || !root.observation.Complete || root.observation.Failure != "" {
		return false, nil
	}
	children := pagedview.RangeIndex[TreeItem]{Store: generation, Root: root.page}
	unresolved, err := children.Unresolved(ctx)
	if err != nil || unresolved != 0 {
		return false, nil
	}
	loaded := time.Now()
	stale, err := s.verifyStructuralStamps(ctx, generation)
	if err != nil {
		if ctx.Err() == nil {
			slog.DebugContext(ctx, "Rebuilding structural cache", "root", s.root.ID, "error", err)
		}
		return false, nil
	}
	releasePublication, err := s.acquireStructurePublication(ctx)
	if err != nil {
		return false, nil
	}
	defer releasePublication()
	s.mu.Lock()
	if !s.pins.drained && s.structure.id < headGeneration-1 && s.structure.directories.Len() == 0 {
		generation.id = s.structure.id + 1
		s.installStructureLocked(generation)
		retained = true
	}
	s.mu.Unlock()
	if !retained {
		return false, nil
	}
	slog.DebugContext(ctx, "Structural checkpoint restored", "root", s.root.ID, "directories", generation.directories.Len(),
		"stale", len(stale), "load_ms", loaded.Sub(started).Milliseconds(), "verify_ms", time.Since(loaded).Milliseconds())
	s.stores.Directories.navigationChanged(s.root)
	return true, stale
}

// verifyStructuralStamps stats every checkpointed directory and returns those
// whose stamp moved, whose path is gone, or which never completed. A created or
// deleted directory surfaces through its parent, whose stamp moved with it.
func (s *indexStore) verifyStructuralStamps(ctx context.Context, generation *structuralGeneration) ([]string, error) {
	root, release, err := s.navigation.Acquire(ctx, s.root.Path)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan structuralDirectory, indexBatchSize)
	var mu sync.Mutex
	var stale []string
	var failure error
	fail := func(err error) {
		mu.Lock()
		if failure == nil {
			failure = err
		}
		mu.Unlock()
		cancel()
	}
	var workers sync.WaitGroup
	for range defaultStructuralScanWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			// Admission is taken per batch, so a foreground listing arriving
			// during the restore is not held behind the whole verification.
			var admitted func()
			defer func() {
				if admitted != nil {
					admitted()
				}
			}()
			for verified := 0; ; verified++ {
				if verified%indexBatchSize == 0 {
					if admitted != nil {
						admitted()
					}
					release, err := s.stores.broker.Acquire(ctx, backgroundwork.Request{Key: s.workKey() + ":restore", Lane: s.root.Path,
						Priority: backgroundwork.PriorityProactive, Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory}})
					if err != nil {
						admitted = nil
						fail(err)
						return
					}
					admitted = release
				}
				directory, open := <-work
				if !open {
					return
				}
				if !structuralStampHolds(root, directory.observation) {
					mu.Lock()
					stale = append(stale, directory.observation.Path)
					mu.Unlock()
				}
			}
		}()
	}
	err = generation.directories.Visit(ctx, func(directory structuralDirectory) error {
		select {
		case work <- directory:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(work)
	workers.Wait()
	if err == nil {
		err = failure
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(stale)
	return stale, nil
}

func structuralStampHolds(root *os.Root, observation DirectoryObservation) bool {
	if !observation.Complete || observation.Failure != "" || !observation.Stamp.known() {
		return false
	}
	info, err := root.Lstat(filepath.FromSlash(observation.Path))
	if err != nil || !info.IsDir() {
		return false
	}
	return directoryStampOf(info) == observation.Stamp
}

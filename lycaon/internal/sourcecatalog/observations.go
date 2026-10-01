package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type DirectoryObservation struct {
	baseSequence          int64
	Path                  string
	Sequence, FirstListed int64
	Entries               int
	Invalidation          uint64
	Complete              bool
	Observed              time.Time
	Epoch                 repochange.Epoch
	// Stamp is the directory's own times when its listing began. A restored
	// checkpoint trusts a listing only while the directory still carries it.
	Stamp   DirectoryStamp
	Failure string
}
type DirectoryRead struct {
	Priority backgroundwork.Priority
	Entries  int
}

func (s *indexStore) readObservation(ctx context.Context, dir string) (DirectoryObservation, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryObservation{}, err
	}
	pin, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return DirectoryObservation{}, err
	}
	defer pin.Release()
	record, found, err := pin.value.directories.Get(ctx, dir)
	if err != nil {
		return DirectoryObservation{}, err
	}
	if !found {
		return DirectoryObservation{}, pagedview.ErrMissing
	}
	return record.observation, nil
}

func (c *Catalog) ObserveDirectory(ctx context.Context, projectID string, root Root, dir string, request DirectoryRead) (DirectoryObservation, error) {
	if err := validateObservationDirectory(root, dir); err != nil {
		return DirectoryObservation{}, err
	}
	store, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return DirectoryObservation{}, err
	}
	return c.observeUntil(ctx, store, normalizeDir(dir), request.Entries, request.Priority)
}

func (c *Catalog) observeUntil(ctx context.Context, store *indexStore, dir string, limit int, priority backgroundwork.Priority) (DirectoryObservation, error) {
	releaseInterest := store.observationInterests.join(dir, priority)
	defer releaseInterest()
	for attempt := 0; attempt < 4; attempt++ {
		observation, err := store.observations.Join(ctx, dir, limit, func(value DirectoryObservation) int { return value.Entries }, func(ctx context.Context, publish func(DirectoryObservation), demand func() int) (DirectoryObservation, error) {
			observation, err := c.observeDirectory(ctx, store, dir, publish, demand)
			if err != nil && ctx.Err() == nil && !errors.Is(err, os.ErrInvalid) && !errors.Is(err, errObservationChanged) && !errors.Is(err, backgroundwork.ErrSuperseded) {
				if recorded := store.recordObservationFailure(ctx, observation, err); recorded != nil {
					return observation, errors.Join(err, recorded)
				}
				observation.Failure = err.Error()
			}
			return observation, err
		})
		if errors.Is(err, errObservationChanged) || errors.Is(err, backgroundwork.ErrSuperseded) {
			continue
		}
		if err != nil || observation.Complete || limit > 0 && observation.Entries >= limit {
			return observation, err
		}
	}
	return DirectoryObservation{Path: dir}, errObservationChanged
}

func (c *Catalog) observeDirectory(ctx context.Context, store *indexStore, dir string, publish func(DirectoryObservation), demand func() int) (DirectoryObservation, error) {
	cached, err := store.readObservation(ctx, dir)
	if err == nil && store.observationFresh(cached) {
		return cached, nil
	}
	if err != nil && !errors.Is(err, pagedview.ErrMissing) {
		return cached, err
	}
	observation := DirectoryObservation{Path: dir, baseSequence: cached.Sequence, Invalidation: store.observationMark(dir), Epoch: repochange.CurrentEpoch(store.root.Path)}
	root, release, err := store.acquireNavigation(ctx)
	if err != nil {
		return observation, err
	}
	defer release()
	pin, err := store.retainGeneration(headGeneration, true)
	if err != nil {
		return observation, err
	}
	defer pin.Release()
	builder, err := newStructuralBuilder(store, pin.value)
	if err != nil {
		return observation, err
	}
	defer builder.close()
	directory, err := c.openObservationDirectory(ctx, store, root, dir)
	if err != nil {
		return observation, err
	}
	defer directory.close()
	observation.Stamp = directory.stamp
	reader := directoryBatch{file: directory.entries}
	for {
		finish, err := c.broker.Acquire(ctx, store.observationRequest(dir))
		if err != nil {
			return observation, err
		}
		quantum := indexBatchSize
		if maximum := demand(); maximum > 0 {
			quantum = min(quantum, maximum-observation.Entries)
		}
		if quantum <= 0 {
			finish()
			break
		}
		batch, complete, readErr := reader.read(quantum)
		var nodes []indexNode
		if readErr == nil {
			nodes = readObservationNodes(root, directory, dir, batch)
		}
		finish()
		if readErr != nil {
			return observation, readErr
		}
		observation.Entries += len(batch)
		observation.Complete = complete
		if err := builder.observe(ctx, directoryDiscovery{nodes: nodes, observation: observation}); err != nil {
			return observation, err
		}
		if complete {
			break
		}
	}
	if err := store.publishStructure(ctx, builder, pin.Generation); err != nil {
		return observation, err
	}
	published, err := store.readObservation(ctx, dir)
	if err != nil {
		return observation, err
	}
	publish(published)
	return published, nil
}

func readObservationNodes(root *os.Root, directory *observationDirectory, dir string, batch []directoryEntry) []indexNode {
	nodes := make([]indexNode, 0, len(batch))
	for _, entry := range batch {
		rel := path.Join(dir, entry.Name())
		if directory.privateFilter.Contains(entry.Name()) {
			continue
		}
		physical := path.Join(directory.resolved, entry.Name())
		node := indexNode{path: rel, parent: dir, name: entry.Name(), depth: pathDepth(rel),
			isDir: entry.IsDir(), isSymlink: entry.Type()&os.ModeSymlink != 0,
			regular: entry.Type().IsRegular(), hidden: hiddenIndexPath(rel)}
		if node.isSymlink {
			targetPath, targetErr := observationPath(root, physical)
			if targetErr == nil {
				target, statErr := root.Stat(filepath.FromSlash(targetPath))
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				if statErr == nil {
					node.isDir = target.IsDir()
				}
			}
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func validateObservationDirectory(root Root, dir string) error {
	if filepath.IsAbs(dir) || sandbox.HasParentTraversal(dir) {
		return os.ErrPermission
	}
	if repochange.IsPrivatePath(filepath.Join(root.Path, filepath.FromSlash(dir))) {
		return os.ErrPermission
	}
	return nil
}

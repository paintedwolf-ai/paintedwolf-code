package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Filesystem waits release the writer transaction.
func (w *indexWalk) visitDir(ctx context.Context, dir string) error {
	if reason := w.store.policy.boundaryPath(dir, true); reason != "" {
		if err := w.begin(ctx); err != nil {
			return err
		}
		return w.cut(ctx, indexCut{Dir: dir, Reason: sandbox.BoundaryLazy})
	}
	if err := w.begin(ctx); err != nil {
		return err
	}
	limit, err := w.budget.observationLimit(ctx, w.tx, dir)
	if err != nil {
		return err
	}
	if err := w.releaseForObservation(ctx); err != nil {
		return err
	}
	observation, err := w.directoryObservation(ctx, dir, limit)
	if err != nil {
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrNotExist) {
			if dir != "." {
				if _, parentErr := w.store.stores.Directories.observeUntil(ctx, w.store, normalizeDir(path.Dir(dir)), 0, backgroundwork.PriorityProactive); parentErr != nil {
					return w.faultDir(ctx, dir, parentErr)
				}
			}
		}
		if errors.Is(err, os.ErrInvalid) {
			info, statErr := w.root.Lstat(filepath.FromSlash(dir))
			if statErr != nil {
				return w.faultDir(ctx, dir, statErr)
			}
			return w.writeLeaf(ctx, dir, info)
		}
		return w.faultDir(ctx, dir, err)
	}
	bounded := limit > 0 && observation.Entries >= limit
	if !observation.Complete && !bounded {
		return errors.New("directory discovery ended before its publication completed")
	}
	info, err := w.root.Lstat(filepath.FromSlash(dir))
	if err != nil {
		return w.faultDir(ctx, dir, err)
	}
	if err := w.begin(ctx); err != nil {
		return err
	}
	writer, err := newIndexWriter(ctx, w.tx)
	if err != nil {
		return err
	}
	err = writer.write(ctx, w.node(dir, info))
	writer.close()
	if err != nil {
		return err
	}
	after := ""
	counted := 0
	for {
		if err := w.releaseForObservation(ctx); err != nil {
			return err
		}
		nodes, next, readErr := w.readObservedIndexNodes(ctx, dir, observation.Sequence, after)
		if readErr != nil {
			return readErr
		}
		if next == "" {
			break
		}
		if err := w.begin(ctx); err != nil {
			return err
		}
		cut, chargeErr := w.budget.charge(ctx, w.tx, dir, len(nodes))
		if chargeErr != nil {
			return chargeErr
		}
		if err := w.admitObservedNodes(ctx, nodes, observation.Sequence); err != nil {
			return err
		}
		counted += len(nodes)
		w.pending += len(nodes)
		if cut.fell() {
			return w.cut(ctx, cut)
		}
		if err := w.maybePublish(ctx); err != nil {
			return err
		}
		after = next
	}
	// Count entries excluded from this consumer.
	if err := w.begin(ctx); err != nil {
		return err
	}
	cut, err := w.budget.charge(ctx, w.tx, dir, observation.Entries-counted)
	if err != nil {
		return err
	}
	if cut.fell() {
		return w.cut(ctx, cut)
	}
	if bounded && w.budget.limits.DirectoryEntries > 0 && observation.Entries > w.budget.limits.DirectoryEntries {
		return w.cut(ctx, indexCut{Dir: dir, Reason: sandbox.BoundaryDirectoryCap})
	}
	if _, err = w.tx.ExecContext(ctx, "UPDATE nodes SET refused='' WHERE path=?", dir); err != nil {
		return err
	}
	if err := w.pruneIndexMembership(ctx, dir, observation.Sequence); err != nil {
		return err
	}
	return w.completeDir(ctx, dir, observation.Sequence)
}

func (w *indexWalk) directoryObservation(ctx context.Context, dir string, limit int) (DirectoryObservation, error) {
	observation, err := w.store.readObservation(ctx, dir)
	if err == nil && w.store.observationFresh(observation) {
		return observation, nil
	}
	if err != nil && !errors.Is(err, pagedview.ErrMissing) {
		return DirectoryObservation{}, err
	}
	return w.store.stores.Directories.observeUntil(ctx, w.store, dir, limit, backgroundwork.PriorityProactive)
}

// Each page reads one structural snapshot, then releases it before metadata I/O.
func (w *indexWalk) observedIndexNodes(ctx context.Context, dir string, sequence int64, after string) ([]indexNode, string, error) {
	navigation, err := openNavigation(ctx, w.store, headGeneration)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = navigation.Close() }()
	children, err := navigation.Children(ctx, dir)
	if err != nil {
		return nil, "", err
	}
	entries, err := children.ReadAfter(ctx, after, indexBatchSize)
	if err != nil || len(entries) == 0 {
		return nil, "", err
	}
	nodes := make([]indexNode, 0, len(entries))
	for _, entry := range entries {
		if entry.Value.Sequence == sequence && !skippedIndexPath(entry.Value.Path) {
			nodes = append(nodes, indexNode{path: entry.Value.Path, name: path.Base(entry.Value.Path),
				isDir: directoryOrderKind(entry.Key), isSymlink: entry.Value.Symlink})
		}
	}
	return nodes, entries[len(entries)-1].Key, nil
}

func (w *indexWalk) readObservedIndexNodes(ctx context.Context, dir string, sequence int64, after string) ([]indexNode, string, error) {
	nodes, next, err := w.observedIndexNodes(ctx, dir, sequence, after)
	if err != nil || len(nodes) == 0 {
		return nodes, next, err
	}
	finishMetadata, err := w.store.stores.broker.Acquire(ctx, backgroundwork.Request{
		Lane: w.store.root.Path, Priority: backgroundwork.PriorityProactive,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	if err != nil {
		return nil, "", err
	}
	defer finishMetadata()
	directory, err := w.root.OpenRoot(filepath.FromSlash(dir))
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = directory.Close() }()
	observed := nodes[:0]
	for _, structural := range nodes {
		info, err := directory.Lstat(path.Base(structural.path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		node := w.node(structural.path, info)
		if structural.isSymlink {
			node.isDir = structural.isDir
		}
		observed = append(observed, node)
	}
	return observed, next, nil
}

// Search enrichment never changes structural generations.
func (w *indexWalk) admitObservedNodes(ctx context.Context, nodes []indexNode, sequence int64) error {
	writer, err := newIndexWriter(ctx, w.tx)
	if err != nil {
		return err
	}
	defer writer.close()
	var directories []any
	var directoryValues []string
	for _, node := range nodes {
		if !w.store.policy.selects(node.path, node.isDir) {
			continue
		}
		if node.isDir && w.store.policy.boundaryDir(node.path) != "" {
			node.refused = sandbox.BoundaryLazy
		}
		if node.name == "" {
			continue
		}
		if err := writer.write(ctx, node); err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(ctx, "UPDATE nodes SET first_listed=? WHERE path=?", sequence, node.path); err != nil {
			return err
		}
		if node.isDir && !node.isSymlink && node.refused != sandbox.BoundaryLazy {
			directoryValues = append(directoryValues, "(?,?)")
			directories = append(directories, node.path, w.store.policy.deferDir(node.path))
		}
	}
	if len(directories) > 0 {
		// #nosec G202 -- Only placeholder tuples are composed; frontier values are bound.
		_, err := w.tx.ExecContext(ctx, "INSERT OR IGNORE INTO frontier(path,deferred) VALUES "+strings.Join(directoryValues, ","), directories...)
		return err
	}
	return nil
}

func (w *indexWalk) pruneIndexMembership(ctx context.Context, dir string, sequence int64) error {
	for {
		if err := w.begin(ctx); err != nil {
			return err
		}
		var stale string
		err := w.tx.QueryRowContext(ctx, "SELECT path FROM nodes WHERE parent=? AND first_listed<>? AND indexed=1 LIMIT 1", dir, sequence).Scan(&stale)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := w.removeSubtree(ctx, stale, true); err != nil {
			return err
		}
	}
}

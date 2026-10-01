package sourcecatalog

import (
	"context"
	"errors"
	"path"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
)

var structuralObservationSerial atomic.Int64

func (b *structuralBuilder) item(ctx context.Context, node indexNode) (pagedview.RangeItem[TreeItem], error) {
	kind := "file"
	if node.isDir {
		kind = "directory"
	}
	baseline := TreeRowFingerprint(node.path, kind, node.isSymlink, false, "")
	item := pagedview.RangeItem[TreeItem]{Key: DirectoryOrder(node.name, node.isDir), Value: TreeItem{Path: node.path, Symlink: node.isSymlink},
		Weight: 1, Fingerprint: baseline, BaselineFingerprint: baseline}
	if !node.isDir || node.isSymlink {
		return item, nil
	}
	index, observation, err := b.children(ctx, node.path)
	if err != nil {
		return item, err
	}
	state := stateOf(observation)
	item.Unresolved = 1
	if !state.Listed {
		return item, nil
	}
	size, err := index.Extent(ctx)
	if err != nil {
		return item, err
	}
	body, err := DirectoryBodyFingerprint(ctx, index, node.path, state, true)
	if err != nil {
		return item, err
	}
	item.Fingerprint = TreeRowFingerprint(node.path, kind, false, true, "").Combine(body)
	item.Weight += directoryBodyRows(state, size)
	item.Unresolved, err = directoryUnresolved(ctx, index, state)
	return item, err
}

// Each directory starts with an empty child range, retaining valid descendant records.
func (b *structuralBuilder) observe(ctx context.Context, listing directoryDiscovery) error {
	b.finalized = false
	observation := listing.observation
	dir := observation.Path
	index, previous, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	flags, err := b.directories.Flags(ctx, dir)
	if err != nil {
		return err
	}
	if flags&directoryStarted == 0 {
		index.Root = 0
		observation.Sequence = structuralObservationSerial.Add(1)
		observation.FirstListed = observation.Sequence
		if err := b.directories.UpdateFlags(ctx, dir, directoryDirty|directoryRepair|directoryStarted|directoryBranchesKnown, directoryHasBranches); err != nil {
			return err
		}
	} else {
		observation.Sequence, observation.FirstListed = previous.Sequence, previous.FirstListed
	}
	if err := b.directories.UpdateFlags(ctx, dir, directoryRepair, 0); err != nil {
		return err
	}
	observation.Observed = time.Now()
	items := make([]pagedview.RangeItem[TreeItem], 0, len(listing.nodes))
	for _, node := range listing.nodes {
		if node.isDir && !node.isSymlink {
			if err := b.directories.UpdateFlags(ctx, dir, directoryHasBranches, 0); err != nil {
				return err
			}
		}
		item, err := b.item(ctx, node)
		if err != nil {
			return err
		}
		item.Value.Sequence = observation.Sequence
		items = append(items, item)
	}
	if err := index.SetBatch(ctx, items); err != nil {
		return err
	}
	if err := b.save(ctx, index, observation); err != nil {
		return err
	}
	if observation.Complete {
		return b.revalidate(ctx, dir)
	}
	return nil
}

// Pruning compares complete memberships, so a partial listing cannot remove a child.
func (b *structuralBuilder) prune(ctx context.Context, dir string) error {
	var source pagedview.PageStore[TreeItem] = b
	old, found, err := b.directories.Previous(ctx, dir)
	if err != nil {
		return err
	}
	if !found && b.base != nil {
		old, found, err = b.base.directories.Get(ctx, dir)
		if err != nil {
			return err
		}
		source = b.base
	}
	if !found {
		return nil
	}
	current, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if !observation.Complete {
		return nil
	}
	return visitStructuralDirectoryEdges(ctx, source, old.page, func(entry pagedview.RangeItem[TreeItem]) error {
		now, _, err := current.LocateItem(ctx, entry.Key)
		if err != nil && !errors.Is(err, pagedview.ErrMissing) {
			return err
		}
		if err != nil || now.Value.Symlink != entry.Value.Symlink {
			return b.directories.DeleteSubtree(ctx, entry.Value.Path)
		}
		return nil
	})
}

// Descendants finish first; each affected directory repairs its child summaries once.
func (b *structuralBuilder) finalize(ctx context.Context) error {
	if b.finalized {
		return nil
	}
	err := b.directories.VisitFlags(ctx, directoryRepair, false, func(dir string, _ uint64) error {
		return b.prepareDirectoryRepair(ctx, dir)
	})
	if err != nil {
		return err
	}
	if err := b.directories.VisitFlagKeys(ctx, directoryAncestor, true, func(dir string, flags uint64) error {
		if dir == "." {
			return nil
		}
		return b.weighFlags(ctx, dir, flags)
	}); err != nil {
		return err
	}
	flags, err := b.directories.Flags(ctx, ".")
	if err != nil {
		return err
	}
	if flags&directoryAncestor != 0 {
		if err := b.weighFlags(ctx, ".", flags); err != nil {
			return err
		}
	}
	if err := b.directories.ClearFlags(ctx, directoryRepair|directoryAncestor); err != nil {
		return err
	}
	b.finalized = true
	return nil
}

func (b *structuralBuilder) prepareDirectoryRepair(ctx context.Context, dir string) error {
	if _, found, err := b.directories.Get(ctx, dir); err != nil || !found {
		return err
	}
	if dir != "." {
		parentPath := path.Dir(dir)
		parent, observed, err := b.children(ctx, parentPath)
		if err != nil {
			return err
		}
		flags, err := b.directories.Flags(ctx, parentPath)
		if err != nil {
			return err
		}
		if flags&directoryDirty != 0 && observed.FirstListed > 0 && observed.Complete {
			item, _, err := parent.LocateItem(ctx, DirectoryOrder(path.Base(dir), true))
			if errors.Is(err, pagedview.ErrMissing) || err == nil && item.Value.Symlink {
				return b.directories.DeleteSubtree(ctx, dir)
			}
			if err != nil {
				return err
			}
		}
	}
	if err := b.prune(ctx, dir); err != nil {
		return err
	}
	for current := dir; ; current = path.Dir(current) {
		flags, err := b.directories.Flags(ctx, current)
		if err != nil {
			return err
		}
		if flags&directoryAncestor != 0 {
			break
		}
		if err := b.directories.UpdateFlags(ctx, current, directoryAncestor, 0); err != nil {
			return err
		}
		if current == "." {
			break
		}
	}
	return nil
}

func (b *structuralBuilder) weighFlags(ctx context.Context, dir string, flags uint64) error {
	if flags&directoryBranchesKnown != 0 && flags&directoryHasBranches == 0 {
		return nil
	}
	index, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if observation.Path == "" {
		return nil
	}
	updates := make([]pagedview.RangeItem[TreeItem], 0, indexBatchSize)
	flush := func() error {
		if err := index.SetBatch(ctx, updates); err != nil {
			return err
		}
		updates = updates[:0]
		return nil
	}
	err = visitStructuralDirectoryEdges(ctx, b, index.Root, func(entry pagedview.RangeItem[TreeItem]) error {
		if entry.Value.Symlink {
			return nil
		}
		item, err := b.item(ctx, indexNode{path: entry.Value.Path, name: path.Base(entry.Value.Path), isDir: true})
		if err != nil {
			return err
		}
		item.Value.Sequence = entry.Value.Sequence
		if item != entry {
			updates = append(updates, item)
		}
		if len(updates) == indexBatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	return b.save(ctx, index, observation)
}

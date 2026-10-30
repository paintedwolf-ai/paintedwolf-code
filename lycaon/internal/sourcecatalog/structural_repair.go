package sourcecatalog

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/pagedview"
	"path"
)

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
		if flags&directoryRepair != 0 {
			if err := b.weighFlags(ctx, dir, flags); err != nil {
				return err
			}
		}
		return b.repairParentEdge(ctx, dir)
	}); err != nil {
		return err
	}
	flags, err := b.directories.Flags(ctx, ".")
	if err != nil {
		return err
	}
	if flags&directoryRepair != 0 {
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

// Each changed subtree refreshes its own edge without reading sibling subtrees.
func (b *structuralBuilder) repairParentEdge(ctx context.Context, dir string) error {
	parent, observation, err := b.children(ctx, path.Dir(dir))
	if err != nil {
		return err
	}
	if observation.Path == "" {
		return nil
	}
	entry, _, err := parent.LocateItem(ctx, DirectoryOrder(path.Base(dir), true))
	if errors.Is(err, pagedview.ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.Value.Symlink {
		return nil
	}
	item, err := b.item(ctx, indexNode{path: dir, name: path.Base(dir), isDir: true})
	if err != nil {
		return err
	}
	item.Value.Sequence = entry.Value.Sequence
	if item == entry {
		return nil
	}
	if err := parent.SetBatch(ctx, []pagedview.RangeItem[TreeItem]{item}); err != nil {
		return err
	}
	return b.save(ctx, parent, observation)
}

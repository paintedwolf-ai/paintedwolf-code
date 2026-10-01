package sourcecatalog

import (
	"context"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Publication fixes parent memberships before this walk fills their unknown branches.
func (b *structuralBuilder) completeCoverage(ctx context.Context, root *os.Root, options structuralScanOptions) error {
	frontier := structuralScanQueue{dir: structuralScanSpoolDir(options)}
	defer frontier.close()
	if err := frontier.push([]string{"."}); err != nil {
		return err
	}
	for frontier.len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := frontier.pop()
		if err != nil {
			return err
		}
		children, observation, err := b.children(ctx, dir)
		if err != nil {
			return err
		}
		if observation.Failure != "" {
			continue
		}
		if !observation.Complete {
			if err := b.collectMissingCoverage(ctx, root, dir, options); err != nil {
				return err
			}
			children, observation, err = b.children(ctx, dir)
			if err != nil {
				return err
			}
			if observation.Failure != "" {
				continue
			}
		}
		if err := children.VisitUnresolved(ctx, func(item pagedview.RangeItem[TreeItem]) (bool, error) {
			if !directoryOrderKind(item.Key) || item.Value.Symlink {
				return true, nil
			}
			return true, frontier.push([]string{item.Value.Path})
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *structuralBuilder) collectMissingCoverage(ctx context.Context, root *os.Root, dir string, options structuralScanOptions) error {
	var workspace sync.Mutex
	options.descend = func(child string) (bool, error) {
		workspace.Lock()
		defer workspace.Unlock()
		return b.admitMissingCoverage(ctx, child)
	}
	admitted, err := b.admitMissingCoverage(ctx, dir)
	if err != nil || !admitted {
		return err
	}
	return scanStructureRoot(ctx, root, dir, options, func(listing directoryDiscovery) error {
		workspace.Lock()
		defer workspace.Unlock()
		if listing.observation.Failure != "" {
			return b.retainFailedDirectory(ctx, listing.observation)
		}
		if err := b.observe(ctx, listing); err != nil {
			return err
		}
		if listing.observation.Complete {
			return b.prune(ctx, listing.observation.Path)
		}
		return nil
	})
}

func (b *structuralBuilder) admitMissingCoverage(ctx context.Context, dir string) (bool, error) {
	record, found, err := b.directories.Get(ctx, dir)
	if err != nil {
		return false, err
	}
	if found && (record.observation.Complete || record.observation.Failure != "") {
		return false, nil
	}
	flags, err := b.directories.Flags(ctx, dir)
	if err != nil || flags&directoryCoverageQueued != 0 {
		return false, err
	}
	if found {
		if err := b.directories.SetPrevious(ctx, dir, record); err != nil {
			return false, err
		}
	}
	err = b.directories.UpdateFlags(ctx, dir, directoryCoverageQueued, directoryStarted|directoryBranchesKnown|directoryHasBranches)
	return err == nil, err
}

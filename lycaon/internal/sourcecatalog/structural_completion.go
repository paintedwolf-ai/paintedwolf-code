package sourcecatalog

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Completed structure remains available when foreground listings advance only part of a tree.
func (c *Catalog) OpenCompletedNavigation(ctx context.Context, project string, root Root) (*Navigation, error) {
	store, err := c.indexStore(ctx, project, root)
	if err != nil {
		return nil, err
	}
	pin, err := store.retainCompletedGeneration()
	if errors.Is(err, pagedview.ErrMissing) {
		return nil, pagedview.ErrPreparing
	}
	if err != nil {
		return nil, err
	}
	defer pin.Release()
	return pin.OpenNavigation(ctx)
}

func (s *indexStore) releaseCompletedStructureLocked() {
	if s.completed != nil {
		s.completed.close()
		s.completed = nil
	}
}

// RequestCoverage schedules reconciliation without making readers wait for it.
func (c *Catalog) RequestCoverage(ctx context.Context, project string, root Root, dir string) error {
	if err := validateObservationDirectory(root, dir); err != nil {
		return err
	}
	store, err := c.indexStore(ctx, project, root)
	if err != nil {
		return err
	}
	if err := store.startInventory(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.inventory.dirty == nil {
		store.inventory.dirty = make(map[string]struct{})
	}
	store.inventory.dirty[normalizeDir(dir)] = struct{}{}
	select {
	case store.inventory.wake <- struct{}{}:
	default:
	}
	return nil
}

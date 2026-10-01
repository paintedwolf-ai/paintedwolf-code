package cadence

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// RootAttached admits a new registry attachment before background warming starts.
func (c *Service) RootAttached(root string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.retiredRoots, fspath.CanonicalPath(root))
}

// RetireRoot cancels dispatch and forgets demand after the last project detaches.
// Completed scan records remain available independently of automatic scheduling.
func (c *Service) RetireRoot(ctx context.Context, root string, attached func(context.Context, string) (bool, error)) error {
	if c == nil || c.Store == nil || strings.TrimSpace(root) == "" {
		return nil
	}
	canonical := fspath.CanonicalPath(root)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Check under the same lock as RootAttached so a concurrent attachment wins.
	if attached != nil {
		keep, err := attached(ctx, canonical)
		if err != nil || keep {
			return err
		}
	}
	if c.retiredRoots == nil {
		c.retiredRoots = make(map[string]struct{})
	}
	c.retiredRoots[canonical] = struct{}{}
	for _, cancel := range c.rootDispatches[canonical] {
		cancel()
	}
	rows, err := c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := c.Store.DeleteSeries(ctx, canonical, row.ScannerID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Service) beginRootDispatch(ctx context.Context, canonical, token string) (context.Context, func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, retired := c.retiredRoots[canonical]; retired {
		return ctx, nil, false
	}
	ctx, cancel := context.WithCancel(ctx)
	if c.rootDispatches == nil {
		c.rootDispatches = make(map[string]map[string]context.CancelFunc)
	}
	if c.rootDispatches[canonical] == nil {
		c.rootDispatches[canonical] = make(map[string]context.CancelFunc)
	}
	c.rootDispatches[canonical][token] = cancel
	return ctx, func() {
		cancel()
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.rootDispatches[canonical], token)
		if len(c.rootDispatches[canonical]) == 0 {
			delete(c.rootDispatches, canonical)
		}
	}, true
}

package cadence

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
)

type rootTracker struct {
	retired    map[string]struct{}
	dispatches map[string]map[string]context.CancelFunc
}

func (r *rootTracker) isRetired(canonical string) bool {
	_, ok := r.retired[canonical]
	return ok
}

func (r *rootTracker) attach(canonical string) {
	delete(r.retired, canonical)
}

func (r *rootTracker) retire(canonical string) {
	if r.retired == nil {
		r.retired = make(map[string]struct{})
	}
	r.retired[canonical] = struct{}{}
	for _, cancel := range r.dispatches[canonical] {
		cancel()
	}
}

func (r *rootTracker) beginDispatch(ctx context.Context, mu *sync.Mutex, canonical, token string) (context.Context, func(), bool) {
	mu.Lock()
	defer mu.Unlock()
	if r.isRetired(canonical) {
		return ctx, nil, false
	}
	ctx, cancel := context.WithCancel(ctx)
	if r.dispatches == nil {
		r.dispatches = make(map[string]map[string]context.CancelFunc)
	}
	if r.dispatches[canonical] == nil {
		r.dispatches[canonical] = make(map[string]context.CancelFunc)
	}
	r.dispatches[canonical][token] = cancel
	return ctx, func() {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		delete(r.dispatches[canonical], token)
		if len(r.dispatches[canonical]) == 0 {
			delete(r.dispatches, canonical)
		}
	}, true
}

// RootAttached admits a new registry attachment before background warming starts.
func (c *Service) RootAttached(root string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roots.attach(fspath.CanonicalPath(root))
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
	c.roots.retire(canonical)
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

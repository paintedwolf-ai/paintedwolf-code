package sourcefeed

import (
	"context"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/workscope"
	"sync"
	"sync/atomic"
)

// WatchOwner releases only routing and observer callbacks it still owns.
type WatchOwner struct {
	stopped   atomic.Bool
	work      workscope.Group
	mu        sync.Mutex
	callbacks map[watchKey]*ownedObserver
}
type ownedObserver struct {
	mu    sync.Mutex
	fn    ExternalObserver
	owner *WatchOwner
}

func (o *WatchOwner) observer(key watchKey, fn ExternalObserver) ExternalObserver {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.callbacks == nil {
		o.callbacks = make(map[watchKey]*ownedObserver)
	}
	callback := o.callbacks[key]
	if callback == nil {
		callback = &ownedObserver{owner: o}
		o.callbacks[key] = callback
	}
	callback.mu.Lock()
	callback.fn = fn
	callback.mu.Unlock()
	return callback.observe
}
func (c *ownedObserver) observe(parent context.Context, projectID string, batch ExternalBatch) {
	ctx, finish, err := c.owner.work.Begin(parent)
	if err != nil {
		return
	}
	defer finish()
	c.mu.Lock()
	fn := c.fn
	c.mu.Unlock()
	if fn != nil && ctx.Err() == nil {
		fn(ctx, projectID, batch)
	}
}

// Stop seals copied callback admission and removes this owner's current routing.
func (o *WatchOwner) Stop() {
	o.stopped.Store(true)
	o.work.Stop()
	watchRegMu.Lock()
	var removed []*projectWatch
	for key, watch := range watchers {
		if watch.owner == o {
			delete(watchers, key)
			removed = append(removed, watch)
		}
	}
	var unused []string
	for _, watch := range removed {
		unused = append(unused, unusedWatchRootsLocked(watch)...)
		watch.unbind()
	}
	for _, root := range unused {
		repochange.CloseRoot(root)
	}
	watchRegMu.Unlock()
	for _, watch := range removed {
		watch.changes.close(context.Background())
	}
}

// Wait drains admitted work before dropping every captured host callback.
func (o *WatchOwner) Wait(ctx context.Context) error {
	if err := o.work.Wait(ctx); err != nil {
		return err
	}
	if !o.stopped.Load() {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, callback := range o.callbacks {
		callback.mu.Lock()
		callback.fn = nil
		callback.mu.Unlock()
	}
	o.callbacks = nil
	return nil
}

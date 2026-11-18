package repochange

import "context"

// flushForTestPasses bounds work from continuous producers.
const flushForTestPasses = 8

// FlushForTest synchronously drains pending timers.
func (d *debouncer) FlushForTest(ctx context.Context) {
	if d == nil {
		return
	}
	for range flushForTestPasses {
		d.mu.Lock()
		keys := make([]string, 0, len(d.pending))
		for k, ent := range d.pending {
			if ent.timer != nil {
				ent.timer.Stop()
			}
			keys = append(keys, k)
		}
		d.mu.Unlock()
		if len(keys) == 0 {
			return
		}
		for _, k := range keys {
			d.mu.Lock()
			ent := d.pending[k]
			d.mu.Unlock()
			if ent != nil {
				d.deliver(ctx, k, ent)
			}
		}
	}
}

// ResetDebouncerForTest drains the global debouncer.
func ResetDebouncerForTest(ctx context.Context) {
	processWatchers().debounce.FlushForTest(ctx)
}

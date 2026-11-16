package repochange

import "context"

// FlushForTest synchronously drains pending timers.
func (d *Debouncer) FlushForTest(ctx context.Context) {
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
			d.flush(ctx, k)
		}
	}
}

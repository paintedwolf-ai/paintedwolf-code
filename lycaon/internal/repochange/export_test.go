package repochange

import "context"

// FlushForTest synchronously drains pending timers.
func (d *debouncer) FlushForTest(ctx context.Context) {
	d.flush(ctx)
}

// ResetDebouncerForTest drains the global debouncer.
func ResetDebouncerForTest(ctx context.Context) {
	processWatchers().debounce.FlushForTest(ctx)
}

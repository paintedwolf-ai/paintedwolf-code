package cadence

import (
	"context"
	"log/slog"
	"sync"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
)

// The broker bounds IO within each capture; these slots let another root make
// progress while a large root prepares its generation. Pending work stays in SQL.
const cadenceDispatchConcurrency = 2

func (c *Service) Run(ctx context.Context) error {
	if !c.configured() {
		<-ctx.Done()
		return ctx.Err()
	}
	completed := make(chan string, cadenceDispatchConcurrency)
	active := make(map[string]bool)
	var workers sync.WaitGroup
	defer workers.Wait()
	for ctx.Err() == nil {
		changed := c.Settings.Changed()
		seriesChanged := c.Store.SeriesChanged.Wake()
		c.reconcileActiveSeries(ctx)
		if c.securityOn() && len(active) < cadenceDispatchConcurrency {
			for _, root := range c.dueRoots(ctx) {
				if active[root] {
					continue
				}
				active[root] = true
				workers.Add(1)
				go func() {
					defer workers.Done()
					c.dispatchRoot(ctx, root)
					completed <- root
				}()
				if len(active) == cadenceDispatchConcurrency {
					break
				}
			}
		}
		delay := c.cadenceCfg().ReconcileInterval()
		if c.securityOn() {
			delay = c.nextWake(ctx, delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case root := <-completed:
			delete(active, root)
		case <-seriesChanged:
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
	return ctx.Err()
}

func (c *Service) nextWake(ctx context.Context, recovery time.Duration) time.Duration {
	due, err := c.Store.NextSeriesDue(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch deadline", "error", err)
		return min(recovery, c.cadenceCfg().WriteBurstSettle())
	}
	heartbeat, err := c.Store.OldestDispatchHeartbeat(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch lease", "error", err)
		return min(recovery, c.cadenceCfg().WriteBurstSettle())
	}
	delay := recovery
	if at := due; !at.IsZero() {
		remaining := at.Sub(c.now())
		if remaining <= 0 {
			// Work still due after dispatch could not be claimed. Retry with
			// a delay instead of spinning on an unreadable registry or store.
			remaining = c.cadenceCfg().WriteBurstSettle()
		}
		delay = min(delay, remaining)
	}
	if at := heartbeat; !at.IsZero() {
		remaining := at.Add(scanbase.DispatchClaimTTL).Sub(c.now())
		if remaining <= 0 {
			remaining = c.cadenceCfg().WriteBurstSettle()
		}
		delay = min(delay, remaining)
	}
	return delay
}

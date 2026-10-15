package cadence

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
)

// The broker bounds IO within each capture; these slots let another root make
// progress while a large root prepares its generation. Pending work stays in SQL.
const cadenceDispatchConcurrency = 2

func (c *Service) Run(ctx context.Context) error {
	if !configured(c) {
		<-ctx.Done()
		return ctx.Err()
	}
	completed := make(chan string, cadenceDispatchConcurrency)
	active := make(map[string]bool)
	var workers sync.WaitGroup
	defer workers.Wait()
	planner := seriesPlanner{c: c}
	dispatcher := rootDispatcher{c: c}
	for ctx.Err() == nil {
		changed := c.Settings.Changed()
		seriesChanged := c.Store.SeriesChanged.Wake()
		planner.reconcileActive(ctx)
		if securityOn(c) && len(active) < cadenceDispatchConcurrency {
			for _, root := range dispatcher.dueRoots(ctx) {
				if active[root] {
					continue
				}
				active[root] = true
				workers.Add(1)
				go func() {
					defer workers.Done()
					dispatcher.dispatchRoot(ctx, root)
					completed <- root
				}()
				if len(active) == cadenceDispatchConcurrency {
					break
				}
			}
		}
		delay := cadenceCfg(c).ReconcileInterval()
		if securityOn(c) {
			delay = dispatcher.nextWake(ctx, delay)
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

func (d rootDispatcher) dueRoots(ctx context.Context) []string {
	due, err := d.c.Store.ListDueSeries(ctx, nowUTC(d.c))
	if err != nil {
		slog.WarnContext(ctx, "scan cadence list due", "error", err)
		return nil
	}
	roots := make(map[string]struct{}, len(due))
	for _, row := range due {
		roots[row.CanonicalPath] = struct{}{}
	}
	ordered := make([]string, 0, len(roots))
	for root := range roots {
		ordered = append(ordered, root)
	}
	sort.Strings(ordered)
	return ordered
}

func (d rootDispatcher) nextWake(ctx context.Context, recovery time.Duration) time.Duration {
	cfg := cadenceCfg(d.c)
	due, err := d.c.Store.NextSeriesDue(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch deadline", "error", err)
		return min(recovery, cfg.WriteBurstSettle())
	}
	heartbeat, err := d.c.Store.OldestDispatchHeartbeat(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch lease", "error", err)
		return min(recovery, cfg.WriteBurstSettle())
	}
	now := nowUTC(d.c)
	delay := recovery
	if at := due; !at.IsZero() {
		remaining := at.Sub(now)
		if remaining <= 0 {
			// Work still due after dispatch could not be claimed. Retry with
			// a delay instead of spinning on an unreadable registry or store.
			remaining = cfg.WriteBurstSettle()
		}
		delay = min(delay, remaining)
	}
	if at := heartbeat; !at.IsZero() {
		remaining := at.Add(scanbase.DispatchClaimTTL).Sub(now)
		if remaining <= 0 {
			remaining = cfg.WriteBurstSettle()
		}
		delay = min(delay, remaining)
	}
	return delay
}

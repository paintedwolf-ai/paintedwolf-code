package webresearch

import (
	"context"
	"github.com/lycaon/lycaon/internal/egressgate"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

func runProviderTasks(ctx context.Context, cancel context.CancelFunc, tasks []fanTask, maxResults int) []providerOutcome {
	if len(tasks) == 0 {
		return nil
	}
	if len(tasks) == 1 {
		start := time.Now()
		out := tasks[0].run()
		out.durationMs = time.Since(start).Milliseconds()
		out.hits = sanitizeHits(out.hits)
		return []providerOutcome{out}
	}

	outcomes := make([]providerOutcome, len(tasks))
	done := make([]bool, len(tasks))
	taskCount := len(tasks)
	if taskCount > math.MaxInt32 {
		taskCount = math.MaxInt32
	}
	var pending = int32(taskCount)
	wake := make(chan struct{}, 1)

	// Workers and the coordinator share outcome snapshots under one lock.
	var resultsMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for i, task := range tasks {
		go func(idx int, fn func() providerOutcome) {
			defer wg.Done()
			taskStart := time.Now()
			out := fn()
			out.durationMs = time.Since(taskStart).Milliseconds()
			// Sanitized once here, ahead of index ingest and the fuse, for every
			// provider.
			out.hits = sanitizeHits(out.hits)
			resultsMu.Lock()
			outcomes[idx] = out
			done[idx] = true
			resultsMu.Unlock()
			atomic.AddInt32(&pending, -1)
			select {
			case wake <- struct{}{}:
			default:
			}
		}(i, task.run)
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	fanoutStart := time.Now()
	graceStart := time.Time{}
	for {
		snapOutcomes, snapDone := snapshotOutcomes(&resultsMu, outcomes, done)
		// No fanCancel while Direct runs, so soft hits cannot abort its
		// crawl/verify pipeline.
		if !directTaskPending(tasks, snapDone) {
			if fanOutBudgetMet(tasks, snapOutcomes, snapDone, maxResults) {
				cancel()
				break
			}
			if fanOutPartialHits(tasks, snapOutcomes, snapDone) > 0 {
				if graceStart.IsZero() {
					graceStart = time.Now()
				} else if fanOutGraceExpired(ctx, time.Since(graceStart)) {
					// Preserve siblings waiting for host approval.
					cancel()
					break
				}
			} else if fanOutZeroHitExpired(ctx, time.Since(fanoutStart)) {
				cancel()
				break
			}
		}
		if atomic.LoadInt32(&pending) == 0 {
			break
		}
		graceLeft := time.Duration(0)
		if !graceStart.IsZero() {
			graceLeft = fanOutGraceTimeout - time.Since(graceStart)
			if graceLeft < 0 {
				graceLeft = 0
			}
		}
		wait := 50 * time.Millisecond
		if graceLeft > 0 && graceLeft < wait {
			wait = graceLeft
		}
		timer := time.NewTimer(wait)
		select {
		case <-finished:
			timer.Stop()
			return outcomes
		case <-wake:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			cancel()
			<-finished
			return outcomes
		}
	}
	<-finished
	return outcomes
}

func fanOutGraceExpired(ctx context.Context, elapsed time.Duration) bool {
	return egressgate.From(ctx) == nil && elapsed >= fanOutGraceTimeout
}

func fanOutZeroHitExpired(ctx context.Context, elapsed time.Duration) bool {
	return egressgate.From(ctx) == nil && elapsed >= fanOutZeroHitTimeout
}

// snapshotOutcomes copies the fan-out result slices under the lock so the polling
// loop reads a consistent view of what has completed.
func snapshotOutcomes(mu *sync.Mutex, outcomes []providerOutcome, done []bool) ([]providerOutcome, []bool) {
	mu.Lock()
	defer mu.Unlock()
	return append([]providerOutcome(nil), outcomes...), append([]bool(nil), done...)
}

func directTaskPending(tasks []fanTask, done []bool) bool {
	for i, task := range tasks {
		if task.direct && !done[i] {
			return true
		}
	}
	return false
}

func fanOutBudgetMet(tasks []fanTask, outcomes []providerOutcome, done []bool, maxResults int) bool {
	return len(fuseCompletedOutcomes(tasks, outcomes, done, maxResults)) >= maxResults
}

func fanOutPartialHits(tasks []fanTask, outcomes []providerOutcome, done []bool) int {
	return len(fuseCompletedOutcomes(tasks, outcomes, done, len(outcomes)))
}

func fuseCompletedOutcomes(tasks []fanTask, outcomes []providerOutcome, done []bool, maxResults int) []WebHit {
	completed := make([]providerOutcome, 0, len(outcomes))
	for i, o := range outcomes {
		if !done[i] {
			continue
		}
		if i < len(tasks) && tasks[i].soft {
			continue
		}
		completed = append(completed, o)
	}
	return fuseHits(completed, maxResults)
}

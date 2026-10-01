package webresearch

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/egressgate"
)

// providerSeedGraceTimeout bounds how long direct search waits for more catalog
// provider seed probes once other seed work has already landed (candidates or hits).
var providerSeedGraceTimeout = 1 * time.Second

// providerSeedZeroHitTimeout caps provider seed fan-out when nothing has
// contributed yet, so dead backends don't hold the full per-provider window.
var providerSeedZeroHitTimeout = 3 * time.Second

// discoveryWait is the search loop's blocking edge: it decides whether the
// search is finished, whether to wait for more contributions, and when to cut
// slow provider seed probes once other seed work has landed.
type discoveryWait struct {
	fr                 *frontier
	crawler            *hostCrawler
	stats              *searchStats
	contribWake        chan struct{}
	providerPending    *atomic.Int64
	providerSeedCancel context.CancelFunc
	providerSeedStart  time.Time
	providerGraceStart time.Time
}

func (w *discoveryWait) seedWorkReady() bool {
	if w.fr.hitCount() > 0 {
		return true
	}
	// Index-memory admits alone do not start the provider-seed grace, so weak
	// memory noise cannot cut live seeds before any probe runs.
	return w.fr.hasNonMemoryCandidate()
}

func (w *discoveryWait) cutProviderSeeds(reason string) {
	w.stats.noteProviderSeedCut(reason)
	if w.providerPending.Load() == 0 {
		return
	}
	w.providerSeedCancel()
}

// pause blocks until new work can arrive. It returns false when the search
// should stop: all provider channels and crawls are done, or provider seeds
// were cut after grace / zero-hit cap.
func (w *discoveryWait) pause(ctx context.Context) bool {
	crawlBusy := w.crawler.pending() || w.crawler.undrained()
	if w.providerPending.Load() == 0 && !crawlBusy {
		return false
	}
	if w.providerPending.Load() > 0 {
		if w.seedWorkReady() {
			if w.fr.hitCount() >= w.fr.maxResults {
				w.cutProviderSeeds("budget_met")
			} else {
				if w.providerGraceStart.IsZero() {
					w.providerGraceStart = time.Now()
				}
				remaining := providerSeedGraceTimeout - time.Since(w.providerGraceStart)
				if remaining <= 0 && egressgate.From(ctx) == nil {
					w.cutProviderSeeds("grace")
				} else if remaining <= 0 && egressgate.From(ctx) != nil {
					select {
					case <-w.contribWake:
					case <-w.crawler.updates:
					case <-ctx.Done():
					case <-time.After(50 * time.Millisecond):
					}
					return true
				} else {
					grace := time.NewTimer(remaining)
					defer grace.Stop()
					select {
					case <-w.contribWake:
					case <-w.crawler.updates:
					case <-grace.C:
					case <-ctx.Done():
					}
					return true
				}
			}
		} else {
			remaining := providerSeedZeroHitTimeout - time.Since(w.providerSeedStart)
			if remaining <= 0 && egressgate.From(ctx) == nil {
				w.cutProviderSeeds("zero_hit_cap")
			} else if remaining <= 0 && egressgate.From(ctx) != nil {
				// Pending approval suspends the zero-hit cutoff.
				select {
				case <-w.contribWake:
				case <-w.crawler.updates:
				case <-ctx.Done():
				case <-time.After(50 * time.Millisecond):
				}
				return true
			} else {
				capTimer := time.NewTimer(remaining)
				defer capTimer.Stop()
				select {
				case <-w.contribWake:
				case <-w.crawler.updates:
				case <-capTimer.C:
				case <-ctx.Done():
				}
				return true
			}
		}
	}
	select {
	case <-w.contribWake:
	case <-w.crawler.updates:
	case <-ctx.Done():
	}
	return true
}

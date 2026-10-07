package webresearch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
)

func finalizeDirectSearchHits(scope string, origin searchOrigin, fr *frontier, stats *searchStats, scorer *queryScorer, memoryKeys map[string]struct{}) []WebHit {
	hits := fr.finalHits()
	recordSeen(scope, hits)
	stats.noteMemoryPhase(len(memoryKeys), fr.spent, len(fr.hits))
	if origin == searchOriginUser {
		stats.setResiduals(fr.residualURLs(scorer, residualHandoffCap))
	}
	return hits
}

func finalizeDirectSearchHitsOrErr(scope string, origin searchOrigin, fr *frontier, stats *searchStats, scorer *queryScorer) ([]WebHit, error) {
	hits := fr.finalHits()
	if len(hits) == 0 {
		return nil, fmt.Errorf("no live hits after verification")
	}
	recordSeen(scope, hits)
	if origin == searchOriginUser {
		stats.setResiduals(fr.residualURLs(scorer, residualHandoffCap))
	}
	return hits, nil
}

// search is the un-instrumented pipeline body; memoryFilled reports the
// early return when strong index-memory hits met the budget before crawls ran.
type directSearchSetup struct {
	ops                queryOps
	sctx               context.Context
	cancel             context.CancelFunc
	fr                 *frontier
	crawler            *hostCrawler
	origin             searchOrigin
	req                seedRequest
	merge              *channelMerge
	scorer             *queryScorer
	contribWake        chan struct{}
	emitContribution   func(seedContribution)
	asyncChannels      []seedChannel
	providerPending    atomic.Int64
	providerSeedCtx    context.Context
	providerSeedCancel context.CancelFunc
	providerSeedStart  time.Time
}

func (d *directDiscoverer) beginDirectSearch(ctx context.Context, dreq DirectRequest, stats *searchStats) *directSearchSetup {
	query, maxResults := dreq.Query, dreq.MaxResults
	ops := parseQueryOperators(query)
	sctx, cancel := context.WithCancel(ctx)
	seen := seenURLs(d.memoryScope)
	scorer := newQueryScorer(ops.cleaned, nil, false, dreq.Period)
	scorer.rerank = d.rerank
	scorer.markSeen(seen)
	perHostCap := maxHitsPerHost
	pinned := map[string]struct{}{}
	if len(ops.siteBases) > 0 {
		perHostCap = 0
		pinned = seedHostsSet(ops.siteBases)
	}
	fr := newFrontier(maxResults, perHostCap)
	crawler := newHostCrawler(sctx)
	origin := d.seedOrigin
	if origin == "" {
		origin = searchOriginUser
	}
	req := seedRequest{
		Query: ops.cleaned, Period: dreq.Period, TaskHint: d.taskHint, MaxResults: maxResults,
		Origin: origin, SiteBases: ops.siteBases, PinnedHosts: pinned,
	}
	merge := newChannelMerge(dreq.Period)
	contribWake := make(chan struct{}, 1)
	emit := func(cont seedContribution) {
		merge.apply(cont, fr, crawler, ops, &scorer, seen, stats)
		select {
		case contribWake <- struct{}{}:
		default:
		}
	}
	var async []seedChannel
	for _, ch := range d.constructSeedChannels(req) {
		if ch.ID() == channelMemory {
			_ = ch.Probe(sctx, req, emit)
			continue
		}
		async = append(async, ch)
	}
	providerSeedCtx, providerSeedCancel := context.WithCancel(sctx)
	return &directSearchSetup{
		ops: ops, sctx: sctx, cancel: cancel, fr: fr, crawler: crawler, origin: origin,
		req: req, merge: merge, scorer: scorer, contribWake: contribWake, emitContribution: emit,
		asyncChannels: async, providerSeedCtx: providerSeedCtx,
		providerSeedCancel: providerSeedCancel, providerSeedStart: time.Now(),
	}
}

func (d *directDiscoverer) search(ctx context.Context, dreq DirectRequest, stats *searchStats) ([]WebHit, bool, error) {
	query, maxResults := dreq.Query, dreq.MaxResults
	s := d.beginDirectSearch(ctx, dreq, stats)
	defer func() {
		// Crawls stop before the slot they may hold is released.
		s.cancel()
		s.crawler.wait()
		s.crawler.releaseSlot()
	}()
	defer func() {
		stats.noteFrontier(s.fr.rounds, s.fr.spent, s.fr.budget, s.crawler.launchedCount())
	}()
	defer s.providerSeedCancel()

	if s.origin == searchOriginUser {
		activeUserSearches.Add(1)
		defer activeUserSearches.Add(-1)
		defer func() {
			stats.setDirectOutcome(s.fr.strongHits(), maxResults)
			if d.index == nil {
				return
			}
			identity := curationctx.SessionFrom(ctx)
			// Match the rewarm query key.
			d.index.QueueSearchOutcome(ctx, strings.TrimSpace(query), identity.ProjectID, identity.ProjectDir, s.fr.strongHits(), maxResults)
		}()
	}

	ops, fr, crawler := s.ops, s.fr, s.crawler
	scorer := s.scorer
	origin, merge, contribWake := s.origin, s.merge, s.contribWake
	emitContribution := s.emitContribution
	sctx, cancel := s.sctx, s.cancel
	providerPending := &s.providerPending

	if len(ops.siteBases) > 0 {
		stats.setPhase("crawl")
		emitContribution(seedContribution{
			ChannelID: channelSite,
			Plan:      &seedPlan{seeds: ops.siteBases},
		})
	}

	stats.setPhase("memory")
	var wg sync.WaitGroup
	//nolint:contextcheck // The context chain is stored in struct fields.
	launchSeedChannels(s.providerSeedCtx, &wg, s.asyncChannels, s.req, emitContribution, providerPending, stats, contribWake)

	wait := &discoveryWait{
		fr: fr, crawler: crawler, stats: stats, contribWake: contribWake,
		providerPending: providerPending, providerSeedCancel: s.providerSeedCancel,
		providerSeedStart: s.providerSeedStart,
	}
	memorySlot := false
	for sctx.Err() == nil {
		memoryKeys := merge.memoryKeySnapshot()
		if len(memoryKeys) > 0 && !fr.filledStrong() {
			if !memorySlot {
				if !crawler.slotAcquired() {
					wait.cutProviderSeeds("discover_slot_canceled")
					cancel()
					wg.Wait()
					return nil, false, fmt.Errorf("discover_slot_canceled")
				}
				memorySlot = true
			}
			if fr.hasProbeableMemory(memoryKeys, scorer) {
				stats.setPhase("memory")
				progressed := fr.round(sctx, d, scorer, false) //nolint:contextcheck // sctx is WithCancel of the caller ctx, held on the struct
				if progressed {
					stats.setPhase("frontier")
				}
				if fr.filledStrong() {
					wait.cutProviderSeeds("budget_met")
					cancel()
					wg.Wait()
					hits := finalizeDirectSearchHits(d.memoryScope, origin, fr, stats, scorer, memoryKeys)
					return hits, true, nil
				}
				if fr.plateauReached() {
					stats.noteFrontierCut("plateau")
					wait.cutProviderSeeds("search_done")
					cancel()
					wg.Wait()
					hits, err := finalizeDirectSearchHitsOrErr(d.memoryScope, origin, fr, stats, scorer)
					if err != nil {
						return nil, false, err
					}
					return hits, false, nil
				}
				// No probe progress this turn — fall through to drain crawls /
				// pause instead of spinning on unreprobeable memory.
				if progressed {
					continue
				}
			}
		}

		fr.admit(crawler.drain()...)
		stats.setPhase("frontier")
		progressed := fr.round(sctx, d, scorer, true) //nolint:contextcheck // sctx is WithCancel of the caller ctx, held on the struct
		if fr.filledStrong() {
			wait.cutProviderSeeds("budget_met")
			cancel()
			break
		}
		if fr.plateauReached() {
			stats.noteFrontierCut("plateau")
			wait.cutProviderSeeds("search_done")
			cancel()
			break
		}
		if progressed {
			continue
		}
		if !wait.pause(sctx) { //nolint:contextcheck // sctx is WithCancel of the caller ctx, held on the struct
			break
		}
	}
	wg.Wait()

	return finalizeSearchAfterWait(d, origin, merge, fr, stats, scorer)
}

func finalizeSearchAfterWait(d *directDiscoverer, origin searchOrigin, merge *channelMerge, fr *frontier, stats *searchStats, scorer *queryScorer) ([]WebHit, bool, error) {
	plan := merge.finalPlan()
	memoryKeys := merge.memoryKeySnapshot()
	stats.noteMemoryPhase(len(memoryKeys), fr.spent, len(fr.hits))

	hits, err := finalizeDirectSearchHitsOrErr(d.memoryScope, origin, fr, stats, scorer)
	if err != nil {
		if len(plan.seeds) == 0 && len(plan.leads) == 0 && len(fr.candidates) == 0 {
			return nil, false, fmt.Errorf("no seeds")
		}
		return nil, false, err
	}
	return hits, false, nil
}

// launchSeedChannels runs the async provider seed channels under providerSeedCtx
// so discoveryWait can cancel slow probes once other seed work has landed.
func launchSeedChannels(providerSeedCtx context.Context, wg *sync.WaitGroup, channels []seedChannel, req seedRequest, emit func(seedContribution), providerPending *atomic.Int64, stats *searchStats, wake chan struct{}) {
	for _, ch := range channels {
		providerPending.Add(1)
		wg.Add(1)
		go func(ch seedChannel) {
			defer wg.Done()
			start := time.Now()
			err := ch.Probe(providerSeedCtx, req, emit)
			providerPending.Add(-1)
			stats.noteChannelDone(ch.ID(), time.Since(start), err)
			// Wake the loop after the decrement so channel completion is never
			// lost between a contribution wake and the pending-count check.
			select {
			case wake <- struct{}{}:
			default:
			}
		}(ch)
	}
}

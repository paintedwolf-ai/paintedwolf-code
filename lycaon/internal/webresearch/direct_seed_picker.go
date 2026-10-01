package webresearch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
)

var errSeedWarmCap = errors.New("seed warm hourly cap reached")

type seedWarmAdmission func(context.Context) (bool, error)

// seedHedgeDelay is how long the primary seed call runs before a slim parallel
// call hedges its tail latency.
var seedHedgeDelay = 8 * time.Second

func pickSeedsSystemPrompt(ctx context.Context) (string, error) {
	return guidance.RenderCatalog(ctx, guidance.UtilityPickSeedsSystemRef, nil)
}

// pickSeedsCached returns the seed plan for a query, reusing a recent plan for
// the same normalized query so verbatim re-searches skip the LLM round trip.
// A slow primary gets a hedged slim call after seedHedgeDelay; whichever answers
// first wins.
func (d *directDiscoverer) pickSeedsCached(
	ctx context.Context,
	query string,
	period Period,
	maxResults int,
	onHost func(base string, phrases []string),
	admit seedWarmAdmission,
) (seedPlan, error) {
	stats := statsFrom(ctx)
	if plan, ok := cachedSeedPlan(query, period, d.taskHint); ok {
		if stats != nil {
			stats.seedCacheHit.Store(true)
		}
		return plan, nil
	}
	if admit != nil {
		ok, err := admit(ctx)
		if err != nil {
			return seedPlan{}, err
		}
		if !ok {
			return seedPlan{}, errSeedWarmCap
		}
	}
	plan, err := d.pickSeedsHedged(ctx, query, period, maxResults, onHost)
	if err != nil {
		return seedPlan{}, err
	}
	storeSeedPlan(query, period, d.taskHint, plan)
	return plan, nil
}

func (d *directDiscoverer) pickSeedsHedged(ctx context.Context, query string, period Period, maxResults int, onHost func(base string, phrases []string)) (seedPlan, error) {
	if !d.seedSharedProvider {
		return d.pickSeeds(ctx, query, period, seedBudgetForHits(maxResults), seedCallTimeoutSec, onHost)
	}
	stats := statsFrom(ctx)
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type seedResult struct {
		plan  seedPlan
		err   error
		hedge bool
	}
	results := make(chan seedResult, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		plan, err := d.pickSeeds(raceCtx, query, period, seedBudgetForHits(maxResults), seedCallTimeoutSec, onHost)
		results <- seedResult{plan: plan, err: err}
	}()
	go func() {
		defer wg.Done()
		timer := time.NewTimer(seedHedgeDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-raceCtx.Done():
			results <- seedResult{err: raceCtx.Err(), hedge: true}
			return
		}
		if raceCtx.Err() != nil {
			results <- seedResult{err: raceCtx.Err(), hedge: true}
			return
		}
		if stats != nil {
			stats.seedHedge.Store(true)
		}
		plan, err := d.pickSeeds(raceCtx, query, period, slimSeedBudget(maxResults), seedSlimRetryTimeoutSec, nil)
		results <- seedResult{plan: plan, err: err, hedge: true}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var lastErr error
	for r := range results {
		if r.err == nil {
			if stats != nil && r.hedge {
				stats.seedHedgeWin.Store(true)
			}
			cancel()
			return r.plan, nil
		}
		lastErr = r.err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no seed response")
	}
	return seedPlan{}, lastErr
}

func (d *directDiscoverer) pickSeeds(ctx context.Context, query string, period Period, budget seedBudget, timeoutSec int, onHost func(base string, phrases []string)) (seedPlan, error) {
	user, err := buildPickSeedsUserMessage(ctx, query, period, d.taskHint, budget)
	if err != nil {
		return seedPlan{}, err
	}
	raw, err := d.seedCall(ctx, user, timeoutSec, query, onHost)
	if err != nil {
		return seedPlan{}, err
	}
	plan, err := parseSeedsJSON(raw)
	if err != nil {
		return seedPlan{}, err
	}
	plan.mergeLeadHosts()
	return plan, nil
}

// seedCall runs the background seed call. When streaming, each completed host
// in the parsed plan prefix goes to onHost so its crawl overlaps the rest of
// the answer.
func (d *directDiscoverer) seedCall(ctx context.Context, user string, timeoutSec int, query string, onHost func(base string, phrases []string)) (string, error) {
	// Queue before the call deadline starts.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := acquireSeedCallSlot(ctx, d.seedProviderID, d.seedSharedProvider); err != nil {
		return "", err
	}
	defer releaseSeedCallSlot(d.seedProviderID, d.seedSharedProvider)
	system, err := pickSeedsSystemPrompt(ctx)
	if err != nil {
		return "", err
	}
	streamer, ok := d.summarizer.(llm.StreamingSummarizer)
	if !ok || onHost == nil {
		return d.summarizeWithTimeout(ctx, system, user, timeoutSec)
	}
	llmCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	var buf []byte
	lastParsed := 0
	return streamer.SummarizeStream(llmCtx, system, user, seedCallMaxTokens, func(delta string) {
		buf = append(buf, delta...)
		if len(buf)-lastParsed < streamParseStride {
			return
		}
		lastParsed = len(buf)
		plan, err := parseSeedsJSON(string(buf))
		if err != nil {
			return
		}
		// The tail elements may be mid-token truncations repaired by
		// balanceJSON ("www.thev") — never launch a crawl for them.
		plan = dropIncompleteTail(plan)
		phrases := plan.crawlPhrases(query)
		for _, base := range plan.hostBases() {
			onHost(base, phrases)
		}
	})
}

func (d *directDiscoverer) summarizeWithTimeout(ctx context.Context, system, user string, timeoutSec int) (string, error) {
	llmCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	return d.summarizer.Summarize(llmCtx, system, user, seedCallMaxTokens)
}

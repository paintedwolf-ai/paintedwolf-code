package webresearch

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"strings"
	"sync"
	"time"
)

// planPageCandidates turns LLM-proposed path-bearing seed URLs into frontier
// candidates whose anchors are harvested when probed. Leads carry hosts and
// headline words only.
func planPageCandidates(plan seedPlan) []indexCandidate {
	var out []indexCandidate
	for _, seed := range plan.seeds {
		if page := pageSeedURL(seed); page != "" {
			out = append(out, indexCandidate{URL: page, Title: sitemapTitle(page), Source: "llm_seed"})
		}
	}
	return out
}

// mergeHits appends extra hits with canonical-URL dedup, re-enforcing the
// total and per-host bounds across the combined set.
func mergeHits(hits, extra []WebHit, maxResults, perHostCap int) []WebHit {
	seen := make(map[string]struct{}, len(hits))
	perHost := make(map[string]int, len(hits))
	for _, h := range hits {
		seen[canonicalURL(h.URL)] = struct{}{}
		perHost[strings.ToLower(urlHost(h.URL))]++
	}
	for _, h := range extra {
		if len(hits) >= maxResults {
			break
		}
		key := canonicalURL(h.URL)
		if _, ok := seen[key]; ok {
			continue
		}
		host := strings.ToLower(urlHost(h.URL))
		if perHostCap > 0 && perHost[host] >= perHostCap {
			continue
		}
		seen[key] = struct{}{}
		perHost[host]++
		hits = append(hits, h)
	}
	return hits
}

// seedPlan is the parsed seed-call response.
type seedPlan struct {
	seeds  []string
	fresh  bool
	expand []string
	leads  []seedLead
}

// freshFor resolves the recency preference. The seed model judges whether
// recency matters for the subject; a declared past window overrides it.
func (p seedPlan) freshFor(period Period) bool {
	if period.Historical(time.Now()) {
		return false
	}
	return p.fresh
}

// rankPhrases returns lowercase phrases used for match scoring and the
// verification relevance gate. Phrases come from the query, LLM expand lines,
// and lead titles — not host-side tokenization, so any language the model
// supplies works.
func (p seedPlan) rankPhrases(query string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(phrase string) {
		phrase = strings.TrimSpace(strings.ToLower(phrase))
		if len(phrase) < 2 {
			return
		}
		if _, ok := seen[phrase]; ok {
			return
		}
		seen[phrase] = struct{}{}
		out = append(out, phrase)
	}
	add(query)
	for _, phrase := range quotedPhrases(query) {
		add(phrase)
	}
	for _, phrase := range p.expand {
		add(phrase)
	}
	for _, lead := range p.leads {
		add(lead.title)
	}
	return out
}

// crawlPhrases is rankPhrases plus lead-headline n-grams: loose matching for
// discovery, since whole invented headlines rarely match live URLs and every
// candidate still passes strict verification.
func (p seedPlan) crawlPhrases(query string) []string {
	out := p.rankPhrases(query)
	seen := make(map[string]struct{}, len(out))
	for _, phrase := range out {
		seen[phrase] = struct{}{}
	}
	for _, lead := range p.leads {
		for _, gram := range leadTitleNGrams(lead.title) {
			if _, ok := seen[gram]; ok {
				continue
			}
			seen[gram] = struct{}{}
			out = append(out, gram)
		}
	}
	return out
}

// hostBases returns the site roots the plan names — seeds plus lead hosts.
func (p seedPlan) hostBases() []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(base string) {
		if base == "" {
			return
		}
		key := strings.ToLower(base)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, base)
	}
	for _, seed := range p.seeds {
		add(normalizeSiteBase(seed))
	}
	for _, lead := range p.leads {
		add(leadPublisherBase(lead))
	}
	return out
}

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

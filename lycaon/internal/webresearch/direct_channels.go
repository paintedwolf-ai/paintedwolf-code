package webresearch

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

const (
	channelMemory         = "memory"
	channelSite           = "site"
	channelProviderPrefix = "provider:"
	sourceProviderSeed    = "provider_seed"
	// crawlOnlySeedProbePath marks stub provider hits that should launch a host
	// crawl without entering verification — see discovererSeedingURLs in tests.
	crawlOnlySeedProbePath = "/.lycaon-seed-probe"
	// seedGateMaxWait is the pacing-slot queue allowance for provider seed
	// probes. It is short so a paced provider fails fast instead of
	// serializing the whole seed wave behind one slot.
	seedGateMaxWait = 3 * time.Second
)

type searchOrigin string

const (
	searchOriginUser searchOrigin = "user"
	searchOriginWarm searchOrigin = "warm"
)

type seedRequest struct {
	Query string
	// Period is the declared time window. The seed prompt states it so the
	// picker ranks that window rather than inferring one from the wording.
	Period      Period
	TaskHint    string
	MaxResults  int
	Origin      searchOrigin
	SiteBases   []string
	PinnedHosts map[string]struct{}
}

type seedContribution struct {
	ChannelID  string
	Candidates []indexCandidate
	Hosts      []string
	Expand     []string
	Plan       *seedPlan
}

type seedChannel interface {
	ID() string
	Probe(ctx context.Context, req seedRequest, emit func(seedContribution)) error
}

type memoryChannel struct{ d *directDiscoverer }

func (c *memoryChannel) ID() string { return channelMemory }

func (c *memoryChannel) Probe(ctx context.Context, req seedRequest, emit func(seedContribution)) error {
	pinned := req.PinnedHosts
	if len(req.SiteBases) > 0 {
		pinned = seedHostsSet(req.SiteBases)
	}
	candidates := c.d.indexMemoryCandidates(ctx, req.Query, req.MaxResults*2, pinned)
	emit(seedContribution{ChannelID: channelMemory, Candidates: candidates})
	return nil
}

type providerSeedChannel struct {
	d          *directDiscoverer
	providerID string
}

func (c *providerSeedChannel) ID() string { return channelProviderPrefix + c.providerID }

func (c *providerSeedChannel) Probe(ctx context.Context, req seedRequest, emit func(seedContribution)) error {
	p := c.d.registry.Get(c.providerID)
	if p == nil {
		emit(seedContribution{ChannelID: c.ID()})
		return nil
	}
	budget := min(req.MaxResults, 5)
	start := time.Now()
	outcome, cached := cachedProviderSeed(c.providerID, req.Query, budget, c.d.settings)
	if !cached {
		outcome = p.Search(withProviderGateWait(ctx, seedGateMaxWait), c.d.settings, req.Query, budget)
		storeProviderSeed(c.providerID, req.Query, budget, c.d.settings, outcome)
	}
	if !outcome.ok {
		wrlog.Info("seed channel skip",
			"channel", c.ID(),
			"reason", outcome.reason,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		emit(seedContribution{ChannelID: c.ID()})
		return nil
	}
	candidates := make([]indexCandidate, 0, len(outcome.hits))
	hosts := make([]string, 0, len(outcome.hits))
	hostSeen := make(map[string]struct{}, len(outcome.hits))
	for _, hit := range outcome.hits {
		base := normalizeSiteBase(hit.URL)
		key := strings.ToLower(urlHost(base))
		if key != "" {
			if _, ok := hostSeen[key]; !ok {
				hostSeen[key] = struct{}{}
				hosts = append(hosts, base)
			}
		}
		if strings.HasSuffix(strings.Split(hit.URL, "?")[0], crawlOnlySeedProbePath) {
			continue
		}
		candidates = append(candidates, webHitToSeedCandidate(hit, c.providerID))
	}
	observability.LogLatency("webresearch", "seed channel contributed", start,
		"channel", c.ID(),
		"candidates", len(candidates),
		"hosts", len(hosts),
	)
	emit(seedContribution{
		ChannelID:  c.ID(),
		Candidates: candidates,
		Hosts:      hosts,
	})
	return nil
}

func webHitToSeedCandidate(hit WebHit, providerID string) indexCandidate {
	return indexCandidate{
		Title:         hit.Title,
		URL:           hit.URL,
		Notes:         hit.Snippet,
		APISnippet:    hit.Snippet,
		Source:        sourceProviderSeed,
		ChannelID:     channelProviderPrefix + providerID,
		APIProviderID: providerID,
	}
}

// resolveSeedProviders selects the crawler's seed channels from the catalog:
// every default_enabled seeds-role entry that is registered and configured.
// Seed participation is catalog-YAML policy, not a user pref — the settings
// UI only governs which providers answer searches directly.
func resolveSeedProviders(settings Settings, reg *Registry) []string {
	if reg == nil {
		return nil
	}
	cat := reg.Catalog()
	if cat == nil {
		return nil
	}
	var out []string
	for _, entry := range cat.Entries() {
		if !entry.DefaultEnabled || !entry.HasRole(RoleSeeds) {
			continue
		}
		if !reg.Has(entry.ID) {
			continue
		}
		p := reg.Get(entry.ID)
		if p == nil || !p.Configured(settings) {
			continue
		}
		out = append(out, entry.ID)
	}
	return out
}

func (d *directDiscoverer) constructSeedChannels(req seedRequest) []seedChannel {
	var out []seedChannel
	out = append(out, &memoryChannel{d})
	if len(req.SiteBases) > 0 {
		return out
	}
	// Interactive Search never awaits an LLM seed channel — warmSeed /
	// pickSeedsCached own Summarizer work in the background.
	if req.Origin != searchOriginWarm && d.registry != nil {
		for _, id := range resolveSeedProviders(d.settings, d.registry) {
			out = append(out, &providerSeedChannel{d: d, providerID: id})
		}
	}
	return out
}

type channelMerge struct {
	mu   sync.Mutex
	plan seedPlan
	// period is the declared window; it outranks the seed model's freshness call.
	period      Period
	planApplied bool
	memoryKeys  map[string]struct{}
}

func newChannelMerge(period Period) *channelMerge {
	return &channelMerge{period: period, memoryKeys: make(map[string]struct{})}
}

func (m *channelMerge) apply(cont seedContribution, fr *frontier, crawler *hostCrawler, ops queryOps, scorer **queryScorer, seen map[string]struct{}, stats *searchStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats.noteChannelContribution(cont.ChannelID, len(cont.Candidates), len(cont.Hosts))
	if len(cont.Candidates) > 0 {
		if cont.ChannelID == channelMemory {
			for _, c := range cont.Candidates {
				m.memoryKeys[canonicalURL(c.URL)] = struct{}{}
			}
		}
		fr.admit(cont.Candidates...)
	}
	if cont.Plan != nil {
		if m.planApplied {
			return
		}
		m.planApplied = true
		m.plan = *cont.Plan
		planned := newQueryScorer(ops.cleaned, m.plan.rankPhrases(ops.cleaned), m.plan.freshFor(m.period), m.period)
		planned.rerank = (*scorer).rerank
		planned.markSeen(seen)
		*scorer = planned
		if len(ops.siteBases) == 0 {
			fr.setPerHostCap(perHostCapForHostCount(m.plan.seedHostCount()))
		}
		crawlPhrases := m.plan.crawlPhrases(ops.cleaned)
		for _, base := range m.plan.hostBases() {
			crawler.launch(base, crawlPhrases)
		}
		fr.admit(planPageCandidates(m.plan)...)
		return
	}
	if len(cont.Hosts) == 0 {
		return
	}
	phrases := cont.Expand
	if len(phrases) == 0 {
		phrases = append([]string{strings.ToLower(ops.cleaned)}, queryTokens(ops.cleaned)...)
	}
	for _, base := range cont.Hosts {
		crawler.launch(base, phrases)
	}
}

func (m *channelMerge) memoryKeySnapshot() map[string]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]struct{}, len(m.memoryKeys))
	for k, v := range m.memoryKeys {
		out[k] = v
	}
	return out
}

func (m *channelMerge) finalPlan() seedPlan {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plan
}

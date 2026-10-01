package webresearch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	warmTurnTimeout = 90 * time.Second
	warmTopicTokens = 24
	warmTopicLen    = 80

	warmSkipBusy        = "discovery busy"
	warmSkipHourCap     = "hourly seed cap"
	warmSkipNoModel     = "summarizer unavailable"
	warmSkipInteractive = "interactive search active"
	warmSkipNotLive     = "no live session"

	warmMaxPageURLs = 12
)

// Warm activity vocabulary shared with persisted activity rows.
const (
	warmTriggerDeclaredURL = "declared_url"
	warmTriggerSearch      = "search"
	warmTriggerSearchSeed  = "search_seed"
	warmTriggerFetch       = "fetch"
	warmTriggerStack       = "stack_warm"
	warmTriggerRewarm      = "history_rewarm"
	warmTriggerStarved     = "starved_rewarm"
	warmTriggerBootstrap   = "bootstrap"

	warmTierCrawl     = "crawl"
	warmTierSeed      = "seed"
	warmTierCrawlSeed = "crawl+seed"
)

const sourceDeclaredPage = "declared_page"

// Warmer builds and records the web index off the search path.
type Warmer struct {
	index    *webindex.Store
	registry *llm.Registry
	policy   *llm.PolicyStore
	cfg      *ConfigStore
	// Plane limits utility-model concurrency.
	Plane *llm.UtilityPlane
	Cost  cost.CostTracker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sessionMu   sync.Mutex
	sessionWork map[string]map[*warmWork]struct{}
}

type warmWork struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWarmer builds a warmer from shared stores.
func NewWarmer(index *webindex.Store, registry *llm.Registry, policy *llm.PolicyStore, cfg *ConfigStore) *Warmer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Warmer{
		index: index, registry: registry, policy: policy, cfg: cfg, ctx: ctx, cancel: cancel,
		sessionWork: make(map[string]map[*warmWork]struct{}),
	}
}

// Close stops new warms and waits for in-flight ones.
func (w *Warmer) Close() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

func (w *Warmer) mode() WarmingMode {
	// Missing config disables background network work.
	if w.cfg == nil {
		return WarmingOff
	}
	return w.cfg.Warming()
}

func (w *Warmer) caps() WarmCaps {
	if w.cfg == nil {
		return defaultWarmCaps()
	}
	return w.cfg.WarmingCaps()
}

func (w *Warmer) summarizer(projectID, projectDir string) compaction.Summarizer {
	if w.registry == nil || w.policy == nil || !llm.ProviderUtilityCallsEnabled() {
		return nil
	}
	scope := llm.SettingsScopeGlobal
	if projectDir != "" {
		scope = llm.SettingsScopeProject
	}
	policy, err := w.policy.Get(scope, projectDir)
	if err != nil {
		return nil
	}
	ref := llm.SummarizerRef(policy)
	if ref.ProviderID == "" {
		return nil
	}
	if _, err := w.registry.Get(ref.ProviderID); err != nil {
		return nil
	}
	return &llm.RegistrySummarizer{
		Registry:   w.registry,
		Policy:     w.policy,
		Scope:      scope,
		ProjectID:  projectID,
		ProjectDir: projectDir,
		Cost:       w.Cost,
		Purpose:    "warm_seed",
		Class:      llm.UtilityClassOverlay,
		Plane:      w.Plane,
	}
}

func (w *Warmer) summarizerSharesCoordinator(projectDir string) bool {
	if w.policy == nil {
		return true
	}
	scope := llm.SettingsScopeGlobal
	if projectDir != "" {
		scope = llm.SettingsScopeProject
	}
	policy, err := w.policy.Get(scope, projectDir)
	if err != nil {
		return true
	}
	return llm.SummarizerSharesCoordinator(policy)
}

func (w *Warmer) wireDiscovererSeed(d *directDiscoverer, projectID, projectDir string) {
	if d.summarizer == nil {
		d.summarizer = w.summarizer(projectID, projectDir)
	}
	if w.policy == nil {
		d.seedSharedProvider = true
		return
	}
	scope := llm.SettingsScopeGlobal
	if projectDir != "" {
		scope = llm.SettingsScopeProject
	}
	pol, err := w.policy.Get(scope, projectDir)
	if err != nil {
		d.seedSharedProvider = true
		return
	}
	ref := llm.SummarizerRef(pol)
	d.seedProviderID = ref.ProviderID
	d.seedSharedProvider = llm.SummarizerSharesCoordinator(pol)
}

type warmRequest struct {
	trigger    string
	query      string // Bounded FTS and seed query.
	taskHint   string // Full seed context.
	projectDir string
	projectID  string
	sessionID  string
	toolCallID string
	hitURLs    []string
	// Explicit pages get direct probe priority.
	pageURLs []string
	// Post-search seed gate.
	strongHits         int
	maxResults         int
	directParticipated bool
	outcomeQuery       string
}

type warmProbeResult struct {
	pages  int
	probes int
}

// WarmDeclaredURLsAsync warms only URLs explicitly present in the user's request.
func (w *Warmer) WarmDeclaredURLsAsync(urlSource, projectID, projectDir, sessionID string, onDone func(api.IndexWarmingMeta)) {
	if w == nil || w.index == nil || w.mode() == WarmingOff {
		return
	}
	pages := warmPageURLs(urlSource)
	if len(pages) == 0 || w.ctx.Err() != nil {
		return
	}
	query := declaredURLTopic(pages)
	w.warmAsync(sessionID, onDone, func(ctx context.Context) api.IndexWarmingMeta {
		return w.warmQuery(ctx, warmRequest{
			trigger:    warmTriggerDeclaredURL,
			query:      query,
			taskHint:   query,
			projectDir: projectDir,
			projectID:  projectID,
			sessionID:  sessionID,
			pageURLs:   pages,
		})
	})
}

func declaredURLTopic(pages []string) string {
	var hosts []string
	seen := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		host := strings.ToLower(urlHost(page))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return capTokens(strings.Join(hosts, " "), warmTopicTokens)
}

// WarmSearchAsync crawls results and may seed an underfilled search.
func (w *Warmer) WarmSearchAsync(searchQuery, projectID, projectDir, sessionID, toolCallID string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool, onDone func(api.IndexWarmingMeta)) {
	if w == nil || w.index == nil || w.mode() == WarmingOff {
		return
	}
	searchQuery = strings.TrimSpace(searchQuery)
	if searchQuery == "" || w.ctx.Err() != nil {
		return
	}
	if len(residualURLs) > warmMaxPageURLs {
		residualURLs = residualURLs[:warmMaxPageURLs]
	}
	w.warmAsync(sessionID, onDone, func(ctx context.Context) api.IndexWarmingMeta {
		return w.warmQuery(ctx, warmRequest{
			trigger:            warmTriggerSearch,
			query:              capTokens(searchQuery, warmTopicTokens),
			taskHint:           searchQuery,
			outcomeQuery:       searchQuery,
			projectDir:         projectDir,
			projectID:          projectID,
			sessionID:          sessionID,
			toolCallID:         toolCallID,
			hitURLs:            hitURLs,
			pageURLs:           residualURLs,
			strongHits:         strongHits,
			maxResults:         maxResults,
			directParticipated: directParticipated,
		})
	})
}

// WarmFetchAsync crawls sibling pages around a fetched page.
func (w *Warmer) WarmFetchAsync(pageURL, title, projectID, projectDir, sessionID, toolCallID string, onDone func(api.IndexWarmingMeta)) {
	if w == nil || w.index == nil || w.mode() == WarmingOff {
		return
	}
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" || w.ctx.Err() != nil {
		return
	}
	topic := strings.TrimSpace(title)
	if topic == "" {
		topic = sitemapTitle(pageURL)
	}
	if topic == "" {
		topic = urlHost(pageURL)
	}
	w.warmAsync(sessionID, onDone, func(ctx context.Context) api.IndexWarmingMeta {
		return w.warmQuery(ctx, warmRequest{
			trigger:    warmTriggerFetch,
			query:      capTokens(topic, warmTopicTokens),
			taskHint:   topic,
			projectDir: projectDir,
			projectID:  projectID,
			sessionID:  sessionID,
			toolCallID: toolCallID,
			hitURLs:    []string{pageURL},
		})
	})
}

// warmPageURLs extracts unique explicit page URLs.
func warmPageURLs(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, raw := range scrapeURLs(text) {
		page := normalizePageURL(raw)
		if page == "" {
			continue
		}
		key := canonicalURL(page)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, page)
		if len(out) >= warmMaxPageURLs {
			break
		}
	}
	return out
}

func (w *Warmer) warmAsync(sessionID string, onDone func(api.IndexWarmingMeta), run func(context.Context) api.IndexWarmingMeta) {
	ctx, cancel := context.WithTimeout(w.ctx, warmTurnTimeout)
	work := &warmWork{cancel: cancel, done: make(chan struct{})}
	w.sessionMu.Lock()
	if w.sessionWork[sessionID] == nil {
		w.sessionWork[sessionID] = make(map[*warmWork]struct{})
	}
	w.sessionWork[sessionID][work] = struct{}{}
	w.sessionMu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer w.finishWarm(sessionID, work)
		meta := run(ctx)
		if ctx.Err() == nil && onDone != nil && (meta.Pages > 0 || strings.Contains(meta.Tier, "seed")) {
			onDone(meta)
		}
	}()
}

func (w *Warmer) finishWarm(sessionID string, work *warmWork) {
	w.sessionMu.Lock()
	delete(w.sessionWork[sessionID], work)
	if len(w.sessionWork[sessionID]) == 0 {
		delete(w.sessionWork, sessionID)
	}
	close(work.done)
	w.sessionMu.Unlock()
	work.cancel()
}

// CancelSession cancels and joins a session's index warms.
func (w *Warmer) CancelSession(sessionID string) {
	if w == nil {
		return
	}
	w.sessionMu.Lock()
	works := make([]*warmWork, 0, len(w.sessionWork[sessionID]))
	for work := range w.sessionWork[sessionID] {
		works = append(works, work)
		work.cancel()
	}
	w.sessionMu.Unlock()
	for _, work := range works {
		<-work.done
	}
}

func (w *Warmer) warmQuery(ctx context.Context, req warmRequest) api.IndexWarmingMeta {
	ctx = curationctx.WithoutLane(curationctx.WithSession(ctx, curationctx.Session{
		SessionID:  req.sessionID,
		ProjectID:  req.projectID,
		ToolCallID: req.toolCallID,
		ProjectDir: req.projectDir,
	}))
	start := time.Now()
	topic := clipTopic(req.query)
	meta := api.IndexWarmingMeta{Trigger: req.trigger, Topic: topic}

	if !tryAcquireDirectSlot() {
		w.index.QueueActivity(ctx, webindex.WarmActivity{Trigger: req.trigger, Topic: topic, SkipReason: warmSkipBusy, SessionID: req.sessionID, ToolCallID: req.toolCallID})
		meta.SkipReason = warmSkipBusy
		return meta
	}
	defer releaseDirectSlot()

	caps := w.caps()
	d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, taskHint: req.taskHint, seedOrigin: searchOriginWarm}

	crawlHosts, crawlResult := w.warmCrawlTier(ctx, d, req, caps)
	crawlPages := crawlResult.pages
	// Preserve useful activity when a crawl is empty.
	if len(crawlHosts) > 0 || crawlPages > 0 {
		w.index.QueueActivity(ctx, webindex.WarmActivity{
			Trigger: req.trigger, Tier: warmTierCrawl, Topic: topic,
			Hosts: crawlHosts, Pages: crawlPages,
			DurationMs: time.Since(start).Milliseconds(), SessionID: req.sessionID, ToolCallID: req.toolCallID,
		})
	}
	meta.Tier = warmTierCrawl
	meta.Hosts = crawlHosts
	meta.Pages = crawlPages

	// Underfilled direct searches may seed. Declared URLs stay on the named hosts.
	underfilled := req.directParticipated && req.maxResults > 0 && req.strongHits*2 < req.maxResults
	seedEligible := w.mode() == WarmingFull &&
		req.trigger == warmTriggerSearch && underfilled
	if seedEligible && interactiveSearchActive() && w.summarizerSharesCoordinator(req.projectDir) {
		seedTrigger := req.trigger
		if req.trigger == warmTriggerSearch {
			seedTrigger = warmTriggerSearchSeed
		}
		w.index.QueueActivity(ctx, webindex.WarmActivity{
			Trigger: seedTrigger, Tier: warmTierSeed, Topic: topic,
			SkipReason: warmSkipInteractive, SessionID: req.sessionID,
		})
		if meta.Pages == 0 && meta.SkipReason == "" {
			meta.SkipReason = warmSkipInteractive
		}
	} else if seedEligible {
		seedStart := time.Now()
		seedHosts, seedResult, skip := w.warmSeed(ctx, d, req.query, req.projectID, req.projectDir, caps, caps.SeedProbes)
		seedPages := seedResult.pages
		seedTrigger := req.trigger
		if req.trigger == warmTriggerSearch {
			seedTrigger = warmTriggerSearchSeed
		}
		w.index.QueueActivity(ctx, webindex.WarmActivity{
			Trigger: seedTrigger, Tier: warmTierSeed, Topic: topic,
			Hosts: seedHosts, Pages: seedPages,
			DurationMs: time.Since(seedStart).Milliseconds(), SkipReason: skip,
			SessionID: req.sessionID,
		})
		if skip == "" {
			meta.Tier = warmTierCrawlSeed
			meta.Hosts = unionHosts(meta.Hosts, seedHosts)
			meta.Pages += seedPages
			if req.trigger == warmTriggerSearch {
				markQuery := req.outcomeQuery
				if markQuery == "" {
					markQuery = req.taskHint
				}
				w.index.MarkQueryRewarmed(ctx, markQuery, req.projectID)
			}
		} else if meta.Pages == 0 && meta.SkipReason == "" {
			meta.SkipReason = skip
		}
	}

	meta.DurationMs = time.Since(start).Milliseconds()
	wrlog.Debug("index warm finished",
		"trigger", req.trigger, "tier", meta.Tier, "topic", topic,
		"hosts", len(meta.Hosts), "pages", meta.Pages,
		"skip", meta.SkipReason, "duration_ms", meta.DurationMs,
		"session_id", req.sessionID)
	return meta
}

// warmCrawlTier probes explicit URLs before topic matches.
func (w *Warmer) warmCrawlTier(ctx context.Context, d *directDiscoverer, req warmRequest, caps WarmCaps) (hosts []string, result warmProbeResult) {
	urls := append(append([]string{}, req.hitURLs...), req.pageURLs...)
	bases, hosts := w.collectWarmCrawlBases(ctx, req.query, urls, caps.TurnHosts)
	extra := declaredPageCandidates(req.pageURLs)
	result = w.warmSiteBases(ctx, d, req.query, bases, caps.TurnProbes, extra...)
	return hosts, result
}

// declaredPageCandidates prioritizes explicit pages.
func declaredPageCandidates(urls []string) []indexCandidate {
	out := make([]indexCandidate, 0, len(urls))
	for _, raw := range urls {
		if page := normalizePageURL(raw); page != "" {
			out = append(out, indexCandidate{URL: page, Title: sitemapTitle(page), Source: sourceDeclaredPage})
		}
	}
	return out
}

func (w *Warmer) collectWarmCrawlBases(ctx context.Context, query string, hitURLs []string, hostCap int) (bases, hosts []string) {
	urls := append([]string{}, hitURLs...)
	if w.index != nil {
		if docs, err := w.index.Search(ctx, query, 12); err == nil {
			for _, doc := range docs {
				urls = append(urls, doc.URL)
			}
		}
	}
	bases, hosts = collectWarmBasesFromURLs(urls)
	if hostCap > 0 && len(hosts) > hostCap {
		bases, hosts = bases[:hostCap], hosts[:hostCap]
	}
	return bases, hosts
}

func collectWarmBasesFromURLs(urls []string) (bases, hosts []string) {
	seen := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		base := normalizeSiteBase(raw)
		host := strings.ToLower(urlHost(base))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
		bases = append(bases, base)
	}
	return bases, hosts
}

// warmSiteBases crawls site indexes and probes explicit candidates first.
func (w *Warmer) warmSiteBases(ctx context.Context, d *directDiscoverer, query string, bases []string, probeBudget int, extra ...indexCandidate) warmProbeResult {
	if (len(bases) == 0 && len(extra) == 0) || probeBudget <= 0 {
		return warmProbeResult{}
	}
	crawler := newHostCrawlerPreSlotted(ctx)
	phrases := append([]string{strings.ToLower(query)}, queryTokens(query)...)
	for _, base := range bases {
		crawler.launch(base, phrases)
	}
	// Warm scoring has no result window.
	scorer := newQueryScorer(query, nil, false, CurrentPeriod())
	fr := newFrontier(probeBudget, maxHitsPerHost)
	fr.budget = probeBudget
	fr.admit(extra...)
	w.runWarmFrontier(ctx, crawler, fr, d, scorer)
	return warmProbeResult{pages: len(fr.hits), probes: fr.spent}
}

// warmDocHosts gives declared pages probe priority within the shared budget.
func (w *Warmer) warmDocHosts(ctx context.Context, d *directDiscoverer, urls []string, query string, probeBudget int) (hosts []string, result warmProbeResult) {
	if probeBudget <= 0 || len(urls) == 0 {
		return nil, warmProbeResult{}
	}
	var crawlBases []string
	crawlBases, hosts = collectWarmBasesFromURLs(urls)
	extra := declaredPageCandidates(urls)
	probeBudget = min(len(extra)+w.caps().TurnProbes, probeBudget)
	result = w.warmSiteBases(ctx, d, query, crawlBases, probeBudget, extra...)
	return hosts, result
}

// warmSeed crawls and ingests a model-generated seed plan.
func (w *Warmer) warmSeed(ctx context.Context, d *directDiscoverer, query, projectID, projectDir string, caps WarmCaps, probeBudget int) (hosts []string, result warmProbeResult, skip string) {
	w.wireDiscovererSeed(d, projectID, projectDir)
	if d.summarizer == nil {
		return nil, warmProbeResult{}, warmSkipNoModel
	}
	if probeBudget <= 0 {
		return nil, warmProbeResult{}, ""
	}
	crawler := newHostCrawlerPreSlotted(ctx)
	plan, err := d.pickSeedsCached(ctx, query, CurrentPeriod(), probeBudget, crawler.launch, func(ctx context.Context) (bool, error) {
		return w.index.ReserveSeedWarm(ctx, time.Now(), time.Hour, caps.SeedWarmsPerHour)
	})
	if err != nil {
		if errors.Is(err, errSeedWarmCap) {
			return nil, warmProbeResult{}, warmSkipHourCap
		}
		return nil, warmProbeResult{}, clipTopic(err.Error())
	}
	crawlPhrases := plan.crawlPhrases(query)
	for _, base := range plan.hostBases() {
		crawler.launch(base, crawlPhrases)
	}
	scorer := newQueryScorer(query, plan.rankPhrases(query), plan.fresh, CurrentPeriod())
	fr := newFrontier(probeBudget, perHostCapForHostCount(plan.seedHostCount()))
	fr.budget = probeBudget
	fr.admit(planPageCandidates(plan)...)
	w.runWarmFrontier(ctx, crawler, fr, d, scorer)
	for _, base := range plan.hostBases() {
		if h := urlHost(base); h != "" {
			hosts = append(hosts, strings.ToLower(h))
		}
	}
	return hosts, warmProbeResult{pages: len(fr.hits), probes: fr.spent}, ""
}

// runWarmFrontier probes until work, budget, or time is exhausted.
func (w *Warmer) runWarmFrontier(ctx context.Context, crawler *hostCrawler, fr *frontier, d *directDiscoverer, scorer *queryScorer) {
	for ctx.Err() == nil {
		fr.admit(crawler.drain()...)
		progressed := fr.round(ctx, d, scorer, true)
		if fr.spent >= fr.budget {
			return
		}
		if progressed {
			continue
		}
		if !crawler.pending() && !crawler.undrained() {
			return
		}
		select {
		case <-crawler.updates:
		case <-ctx.Done():
			return
		}
	}
}

func capTokens(text string, n int) string {
	fields := strings.Fields(text)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

func clipTopic(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > warmTopicLen {
		return text[:warmTopicLen]
	}
	return text
}

func unionHosts(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, h := range list {
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}

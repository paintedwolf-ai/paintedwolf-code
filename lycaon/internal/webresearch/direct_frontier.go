package webresearch

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/webindex"
)

// frontierProbeFactor scales page-fetch capacity with the hit budget.
const (
	frontierProbeFactor = 3
	frontierProbeMin    = 12
	frontierProbeMax    = 24
	// frontierAnchorNotesLen caps the accumulated anchor aggregate carried on
	// a candidate for scoring and snippets.
	frontierAnchorNotesLen = 480
	// frontierMaxCandidates bounds candidate memory independently of ranking.
	frontierMaxCandidates = 1000
)

func frontierProbeBudget(maxResults int) int {
	return min(frontierProbeMax, max(frontierProbeMin, maxResults*frontierProbeFactor))
}

// frontier ranks live crawl candidates as memory, seeds, and anchors arrive.
type frontier struct {
	// mu guards candidate updates from seed workers and the search loop.
	mu         sync.Mutex
	byURL      map[string]int
	candidates []indexCandidate
	probed     map[string]struct{}
	hits       []WebHit
	// Thin hits retain their crawl score for final ordering.
	rankAt     map[string]float64
	spent      int
	budget     int
	rounds     int
	maxResults int
	perHostCap int
	plateau    bool
}

func newFrontier(maxResults, perHostCap int) *frontier {
	return &frontier{
		byURL:      make(map[string]int),
		probed:     make(map[string]struct{}),
		rankAt:     make(map[string]float64),
		budget:     frontierProbeBudget(maxResults),
		maxResults: maxResults,
		perHostCap: perHostCap,
	}
}

func (f *frontier) admit(cs ...indexCandidate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admitLocked(cs...)
}

// Seed workers update the frontier concurrently with search.
func (f *frontier) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

func (f *frontier) hasNonMemoryCandidate() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.candidates {
		if c.Source != sourceIndexMemory {
			return true
		}
	}
	return false
}

func (f *frontier) setPerHostCap(cap int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.perHostCap = cap
}

func (f *frontier) admitLocked(cs ...indexCandidate) {
	for _, c := range cs {
		key := canonicalURL(c.URL)
		if _, done := f.probed[key]; done {
			continue
		}
		if i, ok := f.byURL[key]; ok {
			// Independent anchor descriptions contribute to ranking and snippets.
			f.candidates[i].Notes = mergeAnchorNotes(f.candidates[i].Notes, c.Title)
			if f.candidates[i].Title == "" {
				f.candidates[i].Title = c.Title
			}
			continue
		}
		if len(f.candidates) >= frontierMaxCandidates {
			continue
		}
		f.byURL[key] = len(f.candidates)
		f.candidates = append(f.candidates, c)
	}
}

// strongHits excludes thin pages so pending crawls can still supply stronger results.
func (f *frontier) strongHits() int {
	n := 0
	for _, h := range f.hits {
		if !strings.Contains(h.Snippet, thinContentNote) {
			n++
		}
	}
	return n
}

// filledStrong reports the early-exit condition: the hit budget is met by
// content-bearing pages, or the probe budget is spent.
func (f *frontier) filledStrong() bool {
	return f.strongHits() >= f.maxResults || f.spent >= f.budget
}

func frontierStrongFloor(maxResults int) int {
	return max(1, maxResults/2)
}

// plateauReached leaves remaining probes for warming after a round adds no strong hits.
func (f *frontier) plateauReached() bool {
	return f.plateau
}

// Content-bearing hits precede thin hits ordered by crawl score.
func (f *frontier) finalHits() []WebHit {
	strong := make([]WebHit, 0, len(f.hits))
	thin := make([]WebHit, 0, len(f.hits))
	for _, h := range f.hits {
		if strings.Contains(h.Snippet, thinContentNote) {
			thin = append(thin, h)
		} else {
			strong = append(strong, h)
		}
	}
	sort.SliceStable(thin, func(i, j int) bool {
		return f.rankAt[canonicalURL(thin[i].URL)] > f.rankAt[canonicalURL(thin[j].URL)]
	})
	out := append(append([]WebHit(nil), strong...), thin...)
	if len(out) > f.maxResults {
		out = out[:f.maxResults]
	}
	return out
}

// round verifies ranked candidates and optionally admits their anchors.
// It returns false when no candidate has a positive rank signal.
func (f *frontier) round(ctx context.Context, d *directDiscoverer, scorer *queryScorer, admitAnchors bool) bool {
	// Ranking shares candidate state with seed workers; network probes run unlocked.
	f.mu.Lock()
	if len(f.candidates) == 0 || f.filledStrong() || ctx.Err() != nil {
		f.mu.Unlock()
		return false
	}
	need := f.maxResults - f.strongHits()
	scorer.weighTerms(f.candidates)
	ranked := probeWorthy(rankCandidates(f.candidates, scorer), scorer)
	batchCap := min(f.budget-f.spent, need+verifyOverfetch)
	if batchCap <= 0 || len(ranked) == 0 {
		f.mu.Unlock()
		return false
	}
	if batchCap > len(ranked) {
		batchCap = len(ranked)
	}
	batch := ranked[:batchCap]
	strongBefore := f.strongHits()
	perHostCap := f.perHostCap
	f.mu.Unlock()

	newHits, expand, dead := verifyHits(ctx, batch, scorer, need, perHostCap)

	f.mu.Lock()
	f.spent += len(batch)
	f.rounds++
	for _, c := range batch {
		key := canonicalURL(c.URL)
		f.probed[key] = struct{}{}
		f.rankAt[key] = c.Score
		delete(f.byURL, key)
	}
	kept := f.candidates[:0]
	for _, c := range f.candidates {
		if _, done := f.probed[canonicalURL(c.URL)]; done {
			continue
		}
		f.byURL[canonicalURL(c.URL)] = len(kept)
		kept = append(kept, c)
	}
	f.candidates = kept

	// Extra capacity lets later strong hits replace thin placeholders.
	f.hits = mergeHits(f.hits, newHits, f.maxResults*2, f.perHostCap)
	if admitAnchors {
		f.admitLocked(expand...)
	}
	if strongAfter := f.strongHits(); strongAfter >= frontierStrongFloor(f.maxResults) && strongAfter == strongBefore {
		f.plateau = true
	}
	f.mu.Unlock()

	// Index feedback touches no frontier state, so keep it outside the lock.
	d.ingestHits(ctx, newHits)
	if d.index != nil {
		// Failed live probes invalidate remembered pages.
		for _, url := range dead {
			d.index.QueueDelete(ctx, url)
		}
	}
	for _, c := range expand {
		d.ingestAnchor(ctx, c)
	}
	return true
}

// residualURLs supplies unprobed candidates for post-search warming.
func (f *frontier) residualURLs(scorer *queryScorer, max int) []string {
	if max <= 0 || len(f.candidates) == 0 {
		return nil
	}
	scorer.weighTerms(f.candidates)
	ranked := probeWorthy(rankCandidates(f.candidates, scorer), scorer)
	out := make([]string, 0, min(max, len(ranked)))
	for _, c := range ranked {
		if _, done := f.probed[canonicalURL(c.URL)]; done {
			continue
		}
		out = append(out, c.URL)
		if len(out) >= max {
			break
		}
	}
	return out
}

// probeWorthy requires lexical evidence beyond provenance and recency bonuses.
func probeWorthy(ranked []indexCandidate, scorer *queryScorer) []indexCandidate {
	out := make([]indexCandidate, 0, len(ranked))
	for _, c := range ranked {
		if scorer != nil && !scorer.probeEligible(c) {
			continue
		}
		if c.Score > 0 {
			out = append(out, c)
		}
	}
	return out
}

func mergeAnchorNotes(notes, text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(notes, text) {
		return notes
	}
	if notes == "" {
		return text
	}
	merged := notes + " — " + text
	if len(merged) > frontierAnchorNotesLen {
		return merged[:frontierAnchorNotesLen]
	}
	return merged
}

func (f *frontier) hasProbeableMemory(keys map[string]struct{}, scorer *queryScorer) bool {
	if len(keys) == 0 {
		return false
	}
	scorer.weighTerms(f.candidates)
	mem := make([]indexCandidate, 0)
	for _, c := range f.candidates {
		key := canonicalURL(c.URL)
		if _, ok := keys[key]; !ok {
			continue
		}
		if _, done := f.probed[key]; done {
			continue
		}
		mem = append(mem, c)
	}
	return len(probeWorthy(mem, scorer)) > 0
}

func (d *directDiscoverer) ingestHits(ctx context.Context, hits []WebHit) {
	if d.index == nil {
		return
	}
	for _, h := range hits {
		d.index.QueuePage(ctx, webindex.Page{
			URL:         h.URL,
			Title:       h.Title,
			Description: h.Snippet,
			Verified:    true,
			Origin:      d.ingestOrigin(),
		})
	}
}

func (d *directDiscoverer) ingestAnchor(ctx context.Context, c indexCandidate) {
	if d.index == nil || strings.TrimSpace(c.Title) == "" {
		return
	}
	d.index.QueueAnchors(ctx, c.URL, []string{c.Title}, d.ingestOrigin())
}

// Warmed candidates remain speculative until verified by a live search.
func (d *directDiscoverer) ingestOrigin() string {
	if d.origin != "" {
		return d.origin
	}
	return webindex.OriginEarned
}

// indexMemoryCandidates retrieves cached candidates for live verification.
func (d *directDiscoverer) indexMemoryCandidates(ctx context.Context, query string, limit int, pinnedHosts map[string]struct{}) []indexCandidate {
	if d.index == nil {
		return nil
	}
	docs, err := d.index.Search(ctx, query, limit)
	if err != nil {
		return nil
	}
	out := make([]indexCandidate, 0, len(docs))
	for _, doc := range docs {
		if len(pinnedHosts) > 0 {
			if _, ok := pinnedHosts[strings.ToLower(doc.Host)]; !ok {
				continue
			}
		}
		title := doc.Title
		if title == "" {
			title = doc.Anchors
		}
		out = append(out, indexCandidate{
			URL:    doc.URL,
			Title:  title,
			Notes:  strings.TrimSpace(strings.Join([]string{doc.Description, doc.Anchors}, " ")),
			Source: sourceIndexMemory,
			Date:   doc.Published,
		})
	}
	return out
}

// hostCrawler delivers each completed host crawl without waiting for other hosts.
type hostCrawler struct {
	ctx       context.Context
	mu        sync.Mutex
	launched  map[string]struct{}
	order     []string
	results   map[string][]indexCandidate
	completed map[string]struct{}
	drained   map[string]struct{}
	// Coalesced completion signals wake the waiting frontier.
	updates chan struct{}

	// The discovery slot spans the first crawl through frontier completion.
	slotOnce sync.Once
	slotHeld bool
}

func newHostCrawler(ctx context.Context) *hostCrawler {
	return &hostCrawler{
		ctx:       ctx,
		launched:  make(map[string]struct{}),
		results:   make(map[string][]indexCandidate),
		completed: make(map[string]struct{}),
		drained:   make(map[string]struct{}),
		updates:   make(chan struct{}, 1),
	}
}

// newHostCrawlerPreSlotted returns a crawler with a pre-acquired discovery slot.
func newHostCrawlerPreSlotted(ctx context.Context) *hostCrawler {
	h := newHostCrawler(ctx)
	h.slotOnce.Do(func() {})
	return h
}

// launch starts one host's index crawl unless it is already running.
func (h *hostCrawler) launch(base string, phrases []string) {
	base = normalizeSiteBase(base)
	if base == "" {
		return
	}
	key := strings.ToLower(base)
	h.mu.Lock()
	if _, ok := h.launched[key]; ok {
		h.mu.Unlock()
		return
	}
	h.launched[key] = struct{}{}
	h.order = append(h.order, key)
	h.mu.Unlock()

	go func() {
		h.ensureSlot()
		local, ok := siteIndexCache.get(base)
		if !ok {
			local = discoverSiteIndexes(h.ctx, base, phrases)
			// Empty crawls remain uncached because transient failures also return no candidates.
			if h.ctx.Err() == nil && len(local) > 0 {
				siteIndexCache.put(base, local)
			}
		}
		h.mu.Lock()
		h.results[key] = local
		h.completed[key] = struct{}{}
		h.mu.Unlock()
		select {
		case h.updates <- struct{}{}:
		default:
		}
	}()
}

// drain returns completed host candidates and empty-crawl roots.
func (h *hostCrawler) drain() []indexCandidate {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []indexCandidate
	for _, key := range h.order {
		if _, done := h.completed[key]; !done {
			continue
		}
		if _, seen := h.drained[key]; seen {
			continue
		}
		h.drained[key] = struct{}{}
		local := h.results[key]
		if len(local) == 0 {
			root := key + "/"
			out = append(out, indexCandidate{URL: root, Title: sitemapTitle(root), Source: "llm_seed"})
			continue
		}
		out = append(out, local...)
	}
	return out
}

func (h *hostCrawler) launchedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.launched)
}

func (h *hostCrawler) pending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.completed) < len(h.launched)
}

func (h *hostCrawler) undrained() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.drained) < len(h.completed)
}

// ensureSlot acquires the global discovery slot exactly once for this search.
func (h *hostCrawler) ensureSlot() {
	h.slotOnce.Do(func() {
		if acquireDirectSlot(h.ctx) == nil {
			h.slotHeld = true
		}
	})
}

func (h *hostCrawler) slotAcquired() bool {
	h.ensureSlot()
	return h.slotHeld
}

// releaseSlot frees the discovery slot if this search acquired it.
func (h *hostCrawler) releaseSlot() {
	h.slotOnce.Do(func() {})
	if h.slotHeld {
		releaseDirectSlot()
		h.slotHeld = false
	}
}

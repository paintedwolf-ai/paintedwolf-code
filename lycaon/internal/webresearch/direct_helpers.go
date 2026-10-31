package webresearch

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/observability"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	grobotstxt "github.com/jimsmart/grobotstxt"
)

// queryOps is a query with its search operators parsed out. Operators are
// host-side contracts; the seed LLM never sees them, since it invents paths
// from them.
type queryOps struct {
	// cleaned is the query with site: tokens removed; quoted phrases stay in
	// place so the scorer can extract them.
	cleaned string
	// siteBases are normalized scheme+host bases from site: tokens. When
	// non-empty the seed LLM is skipped entirely and the crawl is pinned here.
	siteBases []string
}

var siteOpRe = regexp.MustCompile(`(?i)(^|\s)site:(\S+)`)

// parseQueryOperators extracts site: tokens from a query. Tokens that don't
// normalize to a fetchable base stay in the query as plain text.
func parseQueryOperators(query string) queryOps {
	ops := queryOps{}
	seen := make(map[string]struct{})
	cleaned := siteOpRe.ReplaceAllStringFunc(query, func(m string) string {
		sub := siteOpRe.FindStringSubmatch(m)
		token := strings.Trim(sub[2], ".,;:!?\"'")
		base := normalizeSiteBase(token)
		if base == "" || !strings.Contains(urlHost(base), ".") {
			return m
		}
		key := strings.ToLower(base)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			ops.siteBases = append(ops.siteBases, base)
		}
		return sub[1]
	})
	ops.cleaned = strings.Join(strings.Fields(cleaned), " ")
	if ops.cleaned == "" {
		ops.cleaned = strings.TrimSpace(query)
	}
	return ops
}

var quotedPhraseRe = regexp.MustCompile(`"([^"]{2,120})"`)

// quotedPhrases extracts explicit multi-word phrases from a query. The scorer
// already builds adjacent-token bigrams; quotes let the caller demand longer
// exact spans.
func quotedPhrases(query string) []string {
	var out []string
	for _, m := range quotedPhraseRe.FindAllStringSubmatch(query, -1) {
		phrase := strings.Join(strings.Fields(strings.ToLower(m[1])), " ")
		if strings.Contains(phrase, " ") {
			out = append(out, phrase)
		}
	}
	return out
}

func searchDirect(ctx context.Context, discoverer DirectDiscoverer, query string, period Period, maxResults int) providerOutcome {
	providerID := directWireProviderID
	if discoverer == nil {
		return providerOutcome{providerID: providerID, reason: "discoverer_unavailable", detail: "no direct search discoverer configured"}
	}
	ctx, cancel := context.WithTimeout(ctx, directChainTimeout)
	defer cancel()

	pageBudget := maxResults
	if pageBudget <= 0 {
		pageBudget = defaultPageBudget
	}

	hits, err := discoverer.Search(ctx, DirectRequest{Query: query, Period: period, MaxResults: pageBudget})
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "search_error", detail: err.Error()}
	}
	if len(hits) == 0 {
		return providerOutcome{providerID: providerID, reason: "fetch_failed", detail: "no live hits"}
	}
	return providerOutcome{providerID: providerID, ok: true, reason: "ok", hits: hits}
}

// directDiscoverSem bounds concurrent direct searches' network activity. A
// search holds one slot from its first phase-2 host crawl through frontier end.
var directDiscoverSem = make(chan struct{}, directDiscoverMaxParallel)

// activeUserSearches counts in-flight user-origin direct searches. The warm
// engine consults it to keep background Summarizer traffic out of interactive
// storms.
var activeUserSearches atomic.Int64

// interactiveSearchActive reports whether any user-origin direct search is in
// flight right now.
func interactiveSearchActive() bool {
	return activeUserSearches.Load() > 0
}

func acquireDirectSlot(ctx context.Context) error {
	select {
	case directDiscoverSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tryAcquireDirectSlot takes a discovery slot only if one is free — the warm
// engine's priority floor: background warming never waits on (or delays) a
// live search.
func tryAcquireDirectSlot() bool {
	select {
	case directDiscoverSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseDirectSlot() {
	select {
	case <-directDiscoverSem:
	default:
		// An unbalanced release, or resetDirectState swapping the semaphore
		// under an in-flight test search; warn rather than panic.
		wrlog.Warn("unbalanced direct-search slot release")
	}
}

// resetDirectState reinitializes the discover semaphore and clears the site
// index cache, seed-plan cache, and seen-URL memory; tests call it for
// isolation.
func resetDirectState(parallel int) {
	if parallel <= 0 {
		parallel = directDiscoverMaxParallel
	}
	directDiscoverSem = make(chan struct{}, parallel)
	resetSeedCallSems()
	activeUserSearches.Store(0)
	resetPoliteness(5 * time.Millisecond)
	siteIndexCache.reset()
	providerSeedCache.reset()
	resetDirectMemory()
}

// seedLead supplies a host to crawl and title words to rank discovered URLs.
type seedLead struct {
	title string
	host  string
}

func normalizeLeadHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimSuffix(raw, "/")
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func leadPublisherBase(lead seedLead) string {
	if h := normalizeLeadHost(lead.host); h != "" {
		return normalizeSiteBase("https://" + h)
	}
	return ""
}

const maxLeadTitleNGrams = 12

// leadTitleNGrams supplies adjacent word pairs and short whole titles for crawl ranking.
// Corpus frequency filters common phrases when they are scored.
func leadTitleNGrams(title string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := tokens[:0]
	for _, tok := range tokens {
		if len(tok) >= 2 {
			kept = append(kept, tok)
		}
	}
	var out []string
	if n := len(kept); n > 0 && n <= 4 {
		out = append(out, strings.Join(kept, " "))
	}
	for i := 0; i+1 < len(kept) && len(out) < maxLeadTitleNGrams; i++ {
		out = append(out, kept[i]+" "+kept[i+1])
	}
	return out
}

func (p *seedPlan) mergeLeadHosts() {
	if len(p.leads) == 0 {
		return
	}
	seen := seedHostsSet(p.seeds)
	for _, lead := range p.leads {
		base := leadPublisherBase(lead)
		if base == "" {
			continue
		}
		host := strings.ToLower(urlHost(base))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		p.seeds = append(p.seeds, base)
		if len(p.seeds) >= maxSeedsPerQuery {
			break
		}
	}
}

func fetchTextURL(ctx context.Context, rawURL string) (string, error) {
	body, status, err := fetchIndexBody(ctx, rawURL)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", io.ErrUnexpectedEOF
	}
	return body, nil
}

func fetchIndexBody(ctx context.Context, rawURL string) (body string, status int, err error) {
	u, err := normalizeFetchURL(rawURL)
	if err != nil {
		return "", 0, err
	}
	stats := statsFrom(ctx)
	if stats != nil && !stats.acquireCrawlFetch() {
		stats.addCrawlFetchCapped()
		return "", 0, context.Canceled
	}
	fetchStart := time.Now()
	status = 0
	defer func() {
		elapsed := time.Since(fetchStart)
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			stats.addCrawlFetchCanceled(elapsed)
		} else {
			stats.addCrawlFetch(elapsed)
		}
		observability.LogWebSearchFetch(observability.WebSearchFetchCapture{
			SearchID: stats.id(),
			Phase:    stats.currentPhase(),
			Kind:     "index",
			URL:      rawURL,
			Host:     strings.ToLower(u.Host),
			Status:   status,
			FetchMs:  elapsed.Milliseconds(),
		})
	}()
	resp, _, err := politeGuardedGet(ctx, u, acceptHeaderForMode("text"), false)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return "", resp.StatusCode, readErr
	}
	return string(raw), resp.StatusCode, nil
}

func fetchRobotsTxt(ctx context.Context, siteBase string) string {
	body, status, err := fetchIndexBody(ctx, siteBase+"/robots.txt")
	if err != nil || status < 200 || status >= 300 {
		return ""
	}
	return body
}

func robotsSitemapURLs(robotsBody, siteBase string) []string {
	if robotsBody == "" {
		return nil
	}
	locs := grobotstxt.Sitemaps(robotsBody)
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		if abs := resolveMaybeRelativeURL(siteBase, loc); abs != "" {
			out = append(out, abs)
		}
	}
	return out
}

func urlAllowedByRobots(robotsBody, pageURL string) bool {
	if robotsBody == "" {
		return true
	}
	return grobotstxt.AgentAllowed(robotsBody, directSearchUserAgent, pageURL)
}

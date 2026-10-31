package webresearch

import (
	"context"
	"encoding/xml"
	"sort"
	"strings"
	"sync"
	"time"
)

func fetchSitemapLinks(ctx context.Context, sitemapURL string, depth int, rankPhrases []string) []siteIndexLink {
	if depth <= 0 || sitemapURL == "" {
		return nil
	}
	body, err := fetchTextURL(ctx, sitemapURL)
	if err != nil {
		return nil
	}
	if strings.Contains(body, "<sitemapindex") {
		var idx struct {
			Sitemaps []struct {
				Loc     string `xml:"loc"`
				LastMod string `xml:"lastmod"`
			} `xml:"sitemap"`
		}
		if err := xml.Unmarshal([]byte(body), &idx); err != nil {
			return nil
		}
		// Query-relevant child sitemaps first (a "/sitemap-reviews.xml" section
		// beats yesterday's news dump for a review query), then recency, so the
		// per-site fetch budget is spent on the sections most likely to match.
		children := make([]siteIndexLink, 0, len(idx.Sitemaps))
		for _, sm := range idx.Sitemaps {
			loc := strings.TrimSpace(sm.Loc)
			if loc == "" {
				continue
			}
			children = append(children, siteIndexLink{URL: loc, Title: sitemapTitle(loc), Date: parseWebDate(sm.LastMod)})
		}
		var out []siteIndexLink
		for _, child := range rankSiteLinks(children, rankPhrases) {
			out = append(out, fetchSitemapLinks(ctx, child.URL, depth-1, rankPhrases)...)
			if len(out) >= maxSitemapFetch {
				return rankSiteLinks(out, rankPhrases)[:maxSitemapFetch]
			}
		}
		return rankSiteLinks(out, rankPhrases)
	}
	var set struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
			// Extension metadata improves ranking beyond the URL slug.
			News struct {
				Title   string `xml:"title"`
				PubDate string `xml:"publication_date"`
			} `xml:"news"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		return nil
	}
	out := make([]siteIndexLink, 0, len(set.URLs))
	for _, u := range set.URLs {
		loc := strings.TrimSpace(u.Loc)
		if loc == "" {
			continue
		}
		title := strings.TrimSpace(u.News.Title)
		if title == "" {
			title = sitemapTitle(loc)
		}
		date := parseWebDate(u.LastMod)
		if date.IsZero() {
			date = parseWebDate(u.News.PubDate)
		}
		out = append(out, siteIndexLink{URL: loc, Title: title, Date: date})
	}
	return rankSiteLinks(out, rankPhrases)[:min(len(out), maxSitemapFetch)]
}

// rankSiteLinks orders index URLs so query-relevant paths surface within the
// per-site fetch budget. Matching uses whole phrases from the query and seed
// plan — not host-side word splitting — and phrases that match most of this
// link set are ignored: they separate nothing here.
func rankSiteLinks(links []siteIndexLink, rankPhrases []string) []siteIndexLink {
	if len(links) == 0 || len(rankPhrases) == 0 {
		return links
	}
	texts := make([]string, len(links))
	for i, link := range links {
		texts[i] = strings.ToLower(link.Title + " " + link.URL)
	}
	rankPhrases = keepDiscriminatingPhrases(rankPhrases, texts)
	type scored struct {
		link  siteIndexLink
		score int
	}
	scoredLinks := make([]scored, len(links))
	for i, link := range links {
		text := texts[i]
		score := 0
		for _, phrase := range rankPhrases {
			if strings.Contains(text, phrase) {
				score += len([]rune(phrase))
			}
		}
		scoredLinks[i] = scored{link: link, score: score}
	}
	// Full ties keep input order (stable sort): indexes and feeds list entries
	// newest-first, and an alphabetical tie-break would fetch "sitemap-2023-…"
	// before "sitemap-2026-07-…" on sites whose sitemap index omits lastmod.
	sort.SliceStable(scoredLinks, func(i, j int) bool {
		if scoredLinks[i].score != scoredLinks[j].score {
			return scoredLinks[i].score > scoredLinks[j].score
		}
		return scoredLinks[i].link.Date.After(scoredLinks[j].link.Date)
	})
	out := make([]siteIndexLink, len(links))
	for i, s := range scoredLinks {
		out[i] = s.link
	}
	return out
}

const maxSnippetLen = 320

const (
	maxSitemapFetch       = 100
	maxSitemapIndexDepth  = 2
	maxRobotsSitemaps     = 5
	maxRSSItems           = 30
	maxHubLinks           = 60
	directSearchUserAgent = "painted-wolf-code-search/1.0"

	// siteCrawlTimeout hard-bounds one site's best-effort index crawl so large
	// sitemap trees leave chain budget for the LLM calls and verification.
	siteCrawlTimeout = 18 * time.Second

	// siteIndexCacheTTL amortizes the crawl across a research session: agents
	// issue bursts of queries over the same hosts, and sitemaps/feeds do not
	// change minute-to-minute.
	siteIndexCacheTTL      = 15 * time.Minute
	siteIndexCacheMaxHosts = 64
)

// indexCandidate is a page URL discovered from a site index.
type indexCandidate struct {
	Title  string
	URL    string
	Notes  string
	Source string
	// ChannelID attributes the first seed channel that contributed this URL.
	ChannelID string
	// APISnippet retains provider search metadata for robots-blocked fallback.
	APISnippet string
	// APIProviderID is the catalog id when Source is provider_seed.
	APIProviderID string
	// Score is the rank score assigned by queryScorer.rankCandidate.
	Score float64
	// Date is the page's lastmod/pubDate when the index declared one.
	Date time.Time
}

type siteIndexLink struct {
	Title string
	URL   string
	Notes string
	Date  time.Time
}

// siteIndexCache holds crawled index pools per site base so repeat queries in a
// session skip the network entirely.
var siteIndexCache = newTTLCache[[]indexCandidate](siteIndexCacheTTL, siteIndexCacheMaxHosts)

// discoverSiteIndexes crawls one site's published indexes. Primary probes
// (robots.txt, llms.txt, /sitemap.xml, homepage) fire concurrently; robots
// filtering applies after collection. Sites with no published index fall back
// to the homepage's own links.
func discoverSiteIndexes(ctx context.Context, siteBase string, rankPhrases []string) []indexCandidate {
	ctx, cancel := context.WithTimeout(ctx, siteCrawlTimeout)
	defer cancel()

	type probeResult struct {
		links  []siteIndexLink
		source string
	}

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		results    []probeResult
		robotsBody string
	)
	collect := func(links []siteIndexLink, source string) {
		if len(links) == 0 {
			return
		}
		mu.Lock()
		results = append(results, probeResult{links: links, source: source})
		mu.Unlock()
	}

	var homepageBody string
	wg.Add(4)
	go func() {
		defer wg.Done()
		robotsBody = fetchRobotsTxt(ctx, siteBase)
		// Cache for frontier probes on this host and honor a declared
		// Crawl-delay for the rest of the session.
		if h := urlHost(siteBase); h != "" {
			storeRobotsBody(h, robotsBody)
			politeSetCrawlDelay(h, robotsCrawlDelay(robotsBody))
		}
		// Sitemaps declared in robots.txt are fetched once robots arrives; the
		// default /sitemap.xml probe already runs concurrently below.
		var inner sync.WaitGroup
		fetched := 0
		for _, sm := range robotsSitemapURLs(robotsBody, siteBase) {
			if strings.EqualFold(strings.TrimRight(sm, "/"), siteBase+"/sitemap.xml") {
				continue
			}
			if fetched >= maxRobotsSitemaps {
				break
			}
			fetched++
			inner.Add(1)
			go func(sm string) {
				defer inner.Done()
				collect(fetchSitemapLinks(ctx, sm, maxSitemapIndexDepth, rankPhrases), "sitemap")
			}(sm)
		}
		inner.Wait()
	}()
	go func() {
		defer wg.Done()
		collect(fetchLLMsTxtLinks(ctx, siteBase+"/llms.txt"), "llms.txt")
	}()
	go func() {
		defer wg.Done()
		collect(fetchSitemapLinks(ctx, siteBase+"/sitemap.xml", maxSitemapIndexDepth, rankPhrases), "sitemap")
	}()
	go func() {
		defer wg.Done()
		// One homepage fetch serves rel=alternate feed autodiscovery and,
		// when every index is empty, the hub-links fallback.
		body, err := fetchTextURL(ctx, siteBase+"/")
		if err != nil {
			return
		}
		homepageBody = body
		var inner sync.WaitGroup
		for _, feedURL := range discoverFeedURLs(body, siteBase) {
			inner.Add(1)
			go func(feedURL string) {
				defer inner.Done()
				collect(fetchFeedLinks(ctx, feedURL, maxRSSItems), "rss")
			}(feedURL)
		}
		inner.Wait()
	}()
	wg.Wait()

	var out []indexCandidate
	appendLinks := func(links []siteIndexLink, source string) {
		for _, link := range links {
			if !urlAllowedByRobots(robotsBody, link.URL) {
				continue
			}
			out = append(out, indexCandidate{
				Title:  link.Title,
				URL:    link.URL,
				Notes:  link.Notes,
				Source: source,
				Date:   link.Date,
			})
		}
	}
	for _, r := range results {
		appendLinks(r.links, r.source)
	}
	if len(out) == 0 {
		appendLinks(parseHubLinks(homepageBody, siteBase), "hub")
	}
	return out
}

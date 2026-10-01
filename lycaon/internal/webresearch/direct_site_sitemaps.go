package webresearch

import (
	"context"
	"encoding/xml"
	"sort"
	"strings"
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

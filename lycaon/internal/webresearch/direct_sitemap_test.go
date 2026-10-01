package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestFetchSitemapLinksRanksChildSitemapsByPhrase(t *testing.T) {
	allowLoopbackFetch(t)
	var (
		mu    sync.Mutex
		order []string
	)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>%s/sitemap-news.xml</loc><lastmod>2026-07-01</lastmod></sitemap>
  <sitemap><loc>%s/sitemap-reviews.xml</loc><lastmod>2026-01-01</lastmod></sitemap>
</sitemapindex>`, srv.URL, srv.URL)
	})
	child := func(name, pageURL string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			fmt.Fprintf(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s</loc><lastmod>2026-06-01</lastmod></url>
</urlset>`, pageURL)
		}
	}
	mux.HandleFunc("/sitemap-news.xml", child("news", srv.URL+"/news/daily-roundup"))
	mux.HandleFunc("/sitemap-reviews.xml", child("reviews", srv.URL+"/reviews/steam-machine-review"))

	links := fetchSitemapLinks(context.Background(), srv.URL+"/sitemap.xml", maxSitemapIndexDepth, []string{"steam machine", "reviews"})
	if len(links) != 2 {
		t.Fatalf("links = %+v want both children crawled", links)
	}
	mu.Lock()
	defer mu.Unlock()
	// The phrase-matching reviews section must be fetched before the fresher
	// but off-topic news section — that order decides which child wins when
	// the per-site budget runs out on large sites.
	if len(order) != 2 || order[0] != "reviews" {
		t.Fatalf("fetch order = %v want reviews first", order)
	}
}

func TestRankSiteLinksKeepsDocumentOrderOnFullTies(t *testing.T) {
	// Polygon-style sitemap index: no lastmod, newest month listed first. An
	// alphabetical tie-break would fetch 2023 before 2026-07.
	links := []siteIndexLink{
		{URL: "https://example.com/sitemap-2026-07-part1-articles.xml", Title: "sitemap 2026 07 part1 articles"},
		{URL: "https://example.com/sitemap-2026-06-part1-articles.xml", Title: "sitemap 2026 06 part1 articles"},
		{URL: "https://example.com/sitemap-2023-01-part1-articles.xml", Title: "sitemap 2023 01 part1 articles"},
	}
	ranked := rankSiteLinks(links, []string{"steam machine reviews"})
	for i, want := range links {
		if ranked[i].URL != want.URL {
			t.Fatalf("ranked[%d] = %s want document order preserved: %+v", i, ranked[i].URL, ranked)
		}
	}
}

func TestHostCrawlerDrainsEveryHost(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	sitemap := func(host string, n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
			for i := 0; i < n; i++ {
				fmt.Fprintf(w, `<url><loc>%s/page-%d</loc></url>`, host, i)
			}
			fmt.Fprint(w, `</urlset>`)
		}
	}
	newSite := func() string {
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		mux.HandleFunc("/sitemap.xml", sitemap(srv.URL, maxSitemapFetch))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		return srv.URL
	}
	seeds := []string{newSite(), newSite(), newSite()}
	crawler := newHostCrawler(context.Background())
	defer crawler.releaseSlot()
	for _, seed := range seeds {
		crawler.launch(seed, []string{"widget docs"})
	}
	var pool []indexCandidate
	for crawler.pending() || crawler.undrained() {
		pool = append(pool, crawler.drain()...)
		if crawler.pending() {
			<-crawler.updates
		}
	}
	counts := map[string]int{}
	for _, c := range pool {
		counts[urlHost(c.URL)]++
	}
	// Every launched host's pool drains in full — no barrier, no crowding.
	for _, seed := range seeds {
		host := urlHost(seed)
		if counts[host] != maxSitemapFetch {
			t.Fatalf("host %s drained %d of %d links: %v", host, counts[host], maxSitemapFetch, counts)
		}
	}
}

func TestFetchSitemapLinksReadsNewsTitles(t *testing.T) {
	allowLoopbackFetch(t)
	body := `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"
        xmlns:news="http://www.google.com/schemas/sitemap-news/0.9">
  <url>
    <loc>https://news.example.com/2026/07/steam-machine-verdict</loc>
    <news:news>
      <news:publication><news:name>Example News</news:name></news:publication>
      <news:publication_date>2026-07-01T08:00:00Z</news:publication_date>
      <news:title>Steam Machine verdict: worth it?</news:title>
    </news:news>
  </url>
</urlset>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	links := fetchSitemapLinks(context.Background(), srv.URL+"/news-sitemap.xml", 1, nil)
	if len(links) != 1 {
		t.Fatalf("links = %+v want 1", links)
	}
	if links[0].Title != "Steam Machine verdict: worth it?" {
		t.Fatalf("title = %q want declared news headline", links[0].Title)
	}
	if links[0].Date.IsZero() || links[0].Date.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("date = %v want news publication date", links[0].Date)
	}
}

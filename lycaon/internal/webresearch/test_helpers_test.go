package webresearch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/webindex"
)

func testHTTPServer(t *testing.T, contentType, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// discovererSeedingURLs builds a user-origin discoverer whose provider seed
// channel admits the given URLs (site roots or page URLs). Summarizer is
// failIfCalled — Search must not invoke the model.
func discovererSeedingURLs(t *testing.T, seedURLs ...string) *directDiscoverer {
	t.Helper()
	const id = "hn"
	hits := make([]WebHit, 0, len(seedURLs))
	for _, u := range seedURLs {
		hitURL := u
		if pageSeedURL(u) == "" {
			// Site-root seeds only launch crawls; a dead probe path on the
			// same host avoids a spurious verified homepage hit.
			hitURL = strings.TrimRight(normalizeSiteBase(u), "/") + crawlOnlySeedProbePath
		}
		title := seedHitLabel(u)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  title,
			Provider: id,
		})
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(&stubSeedProvider{id: id, hits: hits})
	return &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		registry:   reg,
		settings:   testSeedSettings(id),
		seedOrigin: searchOriginUser,
	}
}

// seedHitLabel turns a seed URL into lexical title text so probe admission can
// see path tokens (install.md → "install") the same way a real provider snippet would.
func seedHitLabel(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" || u.Path == "/" {
		return "seed page"
	}
	seg := strings.Trim(u.Path, "/")
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	seg = strings.TrimSuffix(seg, ".md")
	seg = strings.TrimSuffix(seg, ".html")
	seg = strings.ReplaceAll(seg, "-", " ")
	seg = strings.ReplaceAll(seg, "_", " ")
	if seg == "" {
		return "seed page"
	}
	return seg
}

func discovererSeedingURLsWithIndex(t *testing.T, index *webindex.Store, seedURLs ...string) *directDiscoverer {
	t.Helper()
	d := discovererSeedingURLs(t, seedURLs...)
	d.index = index
	return d
}

// providerBackedDiscoverer returns a user-origin discoverer that admits one
// titled hit at liveURL. Summarizer is failIfCalled.
func providerBackedDiscoverer(t *testing.T, providerID, queryTitle, liveURL string) *directDiscoverer {
	t.Helper()
	reg := NewRegistry(testCatalog(t))
	reg.Register(&stubSeedProvider{
		id: providerID,
		hits: []WebHit{{
			Title:    queryTitle,
			URL:      liveURL,
			Snippet:  queryTitle,
			Provider: providerID,
		}},
	})
	return &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		registry:   reg,
		settings:   testSeedSettings(providerID),
		seedOrigin: searchOriginUser,
	}
}

// widgetReviewSite serves a page that verifies for "widget frobnicator review".
func widgetReviewSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review guide"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

package webresearch

import (
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/textguard"
	"golang.org/x/net/html"
)

// sanitizeFetchResult strips invisible codepoints from a fetched body before it
// is cached and indexed; marking happens later at the message seam. Stub and
// empty bodies are left untouched so callers can detect extract failure.
func sanitizeFetchResult(res FetchURLResult) FetchURLResult {
	if fetchBodyUnusable(res.Text) {
		return res
	}
	// Strip only, no trimming: the cached and paged bytes stay the page's own.
	res.Text = textguard.StripInvisibleFormatRunesUntilStable(res.Text)
	return res
}

// fetchBodyUnusable reports an empty or extract-stub body — nothing to sanitize
// or cache.
func fetchBodyUnusable(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "" || trimmed == fetchHTMLExtractStub
}

func fetchProvenanceHost(pageURL string) string {
	u, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || u.Hostname() == "" {
		return "unknown"
	}
	return strings.ToLower(u.Hostname())
}

// isHiddenElement reports structural hiddenness from attributes only — no content
// inspection.
func isHiddenElement(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		key := strings.ToLower(strings.TrimSpace(a.Key))
		val := strings.ToLower(strings.TrimSpace(a.Val))
		switch key {
		case "hidden":
			return true
		case "aria-hidden":
			if val == "true" {
				return true
			}
		case "style":
			if styleHidesContent(val) {
				return true
			}
		}
	}
	return false
}

func styleHidesContent(style string) bool {
	compact := strings.ReplaceAll(style, " ", "")
	return strings.Contains(compact, "display:none") ||
		strings.Contains(compact, "visibility:hidden")
}

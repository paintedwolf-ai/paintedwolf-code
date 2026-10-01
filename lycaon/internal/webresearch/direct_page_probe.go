package webresearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/lycaon/lycaon/internal/webindex"
	"golang.org/x/net/html"
)

// fetchPageProbe GETs a bounded window of the page and extracts title, meta
// description, headings, and a declared publish date.
func fetchPageProbe(ctx context.Context, rawURL string) pageProbe {
	u, err := normalizeFetchURL(rawURL)
	if err != nil {
		return pageProbe{}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, verifyFetchTimeout)
	defer cancel()
	resp, finalURL, err := politeGuardedGet(fetchCtx, u, acceptHeaderForMode("text"), true)
	if err != nil {
		if errors.Is(err, errRobotsBlocked) {
			return pageProbe{robotsBlocked: true}
		}
		return pageProbe{inconclusive: true}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 429 and 5xx say "not now", not "not there".
		return pageProbe{inconclusive: resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, verifyBodyByteLimit))
	if err != nil && len(body) == 0 {
		return pageProbe{inconclusive: true}
	}
	probe := pageProbe{live: true}
	if modified := parseWebDate(resp.Header.Get("Last-Modified")); !modified.IsZero() {
		probe.date = modified
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	text := string(body)
	if strings.Contains(contentType, "html") || strings.HasPrefix(strings.TrimSpace(text), "<") {
		extractProbeMeta(text, finalURL, &probe)
		return probe
	}
	sample := strings.Join(strings.Fields(text), " ")
	if len(sample) > verifyTextSampleLen {
		sample = sample[:verifyTextSampleLen]
	}
	probe.textSample = sample
	return probe
}

// extractProbeMeta pulls title, description, headings, date, in-content
// anchors, and a visible-text sample from (possibly truncated) HTML. An empty
// baseURL skips link harvesting. Sample and links come from fetch_url's
// readability extraction so nav chrome stays out of scoring; pages readability
// can't extract fall back to a whole-document walk.
func extractProbeMeta(raw, baseURL string, probe *pageProbe) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return
	}
	base, baseErr := url.Parse(baseURL)
	haveBase := baseURL != "" && baseErr == nil
	collectProbeMeta(doc, probe)

	if haveBase {
		if art, err := readability.FromReader(strings.NewReader(raw), base); err == nil && art.Node != nil {
			if sample := inlineText(art.Node); len(sample) >= readabilityMinChars {
				probe.mainContent = true
				probe.textSample = clipProbeSample(sample)
				collectProbeLinks(art.Node, base, probe)
				return
			}
		}
	}

	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg":
				return
			case "a":
				if haveBase && len(probe.links) < maxProbeLinks {
					if link, ok := probeLinkFromAnchor(base, n); ok {
						probe.links = append(probe.links, link)
					}
				}
			}
		}
		if n.Type == html.TextNode && text.Len() < verifyTextSampleLen {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	probe.textSample = clipProbeSample(text.String())
}

// collectProbeMeta walks the full document for head-level facts: title, meta
// description, declared dates, and headings. These live outside the article
// node, so they always come from the whole document.
func collectProbeMeta(doc *html.Node, probe *pageProbe) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg":
				return
			case "title":
				if probe.title == "" {
					probe.title = strings.Join(strings.Fields(anchorText(n)), " ")
				}
			case "h1", "h2", "h3":
				if len(probe.headings) < maxProbeHeadings {
					if h := strings.Join(strings.Fields(anchorText(n)), " "); h != "" {
						probe.headings = append(probe.headings, h)
					}
				}
			case "meta":
				name, property, content := metaAttrs(n)
				switch {
				case content == "":
				case name == "description" || property == "og:description":
					if probe.description == "" {
						probe.description = webindex.NormalizeWebTextForStorage(content, 0)
					}
				case property == "article:published_time" || property == "article:modified_time" || name == "date":
					if date := parseWebDate(content); !date.IsZero() && !probe.dateFromPage {
						probe.date = date
						probe.dateFromPage = true
					}
				}
			case "time":
				if !probe.dateFromPage {
					for _, attr := range n.Attr {
						if attr.Key == "datetime" {
							if date := parseWebDate(attr.Val); !date.IsZero() {
								probe.date = date
								probe.dateFromPage = true
							}
							break
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// collectProbeLinks harvests anchors under the article node only — links the
// content chose to make, not the site's navigation.
func collectProbeLinks(n *html.Node, base *url.URL, probe *pageProbe) {
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if len(probe.links) >= maxProbeLinks {
			return
		}
		if x.Type == html.ElementNode && x.Data == "a" {
			if link, ok := probeLinkFromAnchor(base, x); ok {
				probe.links = append(probe.links, link)
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
}

func clipProbeSample(s string) string {
	sample := strings.Join(strings.Fields(s), " ")
	if len(sample) > verifyTextSampleLen {
		sample = sample[:verifyTextSampleLen]
	}
	return sample
}

// probeLinkFromAnchor keeps an anchor when it looks like a content link: an
// absolute-resolvable http(s) URL with wordy link text. Nav chrome ("Store",
// "Sign in") fails the word floor; headline links pass it.
func probeLinkFromAnchor(base *url.URL, n *html.Node) (probeLink, bool) {
	var href string
	for _, attr := range n.Attr {
		if attr.Key == "href" {
			href = strings.TrimSpace(attr.Val)
			break
		}
	}
	if href == "" || strings.HasPrefix(href, "#") {
		return probeLink{}, false
	}
	text := anchorText(n)
	if len(strings.Fields(text)) < minLinkTextWords {
		return probeLink{}, false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return probeLink{}, false
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return probeLink{}, false
	}
	u.Fragment = ""
	return probeLink{text: text, url: u.String()}, true
}

func metaAttrs(n *html.Node) (name, property, content string) {
	for _, attr := range n.Attr {
		switch attr.Key {
		case "name":
			name = strings.ToLower(strings.TrimSpace(attr.Val))
		case "property":
			property = strings.ToLower(strings.TrimSpace(attr.Val))
		case "content":
			content = strings.TrimSpace(attr.Val)
		}
	}
	return name, property, content
}

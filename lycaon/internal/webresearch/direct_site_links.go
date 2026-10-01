package webresearch

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	llmsTxtBulletRe = regexp.MustCompile(`^\s*-\s*\[([^\]]+)\]\(([^)]+)\)\s*:?\s*(.*)$`)
	h2Re            = regexp.MustCompile(`(?m)^##\s+(.+)\s*$`)
)

func fetchLLMsTxtLinks(ctx context.Context, llmsURL string) []siteIndexLink {
	body, err := fetchTextURL(ctx, llmsURL)
	if err != nil {
		return nil
	}
	links := parseLLMSTxtLinks(body)
	out := make([]siteIndexLink, 0, len(links))
	for _, link := range links {
		out = append(out, siteIndexLink{Title: link.Title, URL: link.URL, Notes: link.Notes})
	}
	return out
}

// parseHubLinks scrapes same-host anchors from an already-fetched homepage.
// It is the sparse-site fallback: nav menus and doc indexes are real, current
// URLs.
func parseHubLinks(body, siteBase string) []siteIndexLink {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	base, err := url.Parse(siteBase + "/")
	if err != nil {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	host := strings.ToLower(base.Hostname())
	seen := make(map[string]struct{})
	var out []siteIndexLink
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxHubLinks {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key != "href" {
					continue
				}
				link := resolveHubHref(base, host, attr.Val)
				if link == "" {
					break
				}
				key := canonicalURL(link)
				if _, ok := seen[key]; ok {
					break
				}
				seen[key] = struct{}{}
				title := strings.TrimSpace(anchorText(n))
				if title == "" {
					title = sitemapTitle(link)
				}
				out = append(out, siteIndexLink{Title: title, URL: link})
				break
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// resolveHubHref resolves an anchor href against the hub page, keeping only
// same-host http(s) page links.
func resolveHubHref(base *url.URL, host, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if !strings.EqualFold(u.Hostname(), host) {
		return ""
	}
	if strings.TrimSuffix(u.Path, "/") == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func anchorText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func parseLLMSTxtLinks(body string) []indexCandidate {
	optional := false
	var out []indexCandidate
	for _, line := range strings.Split(body, "\n") {
		if m := h2Re.FindStringSubmatch(line); len(m) == 2 {
			optional = strings.EqualFold(strings.TrimSpace(m[1]), "Optional")
			continue
		}
		if optional {
			continue
		}
		m := llmsTxtBulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		linkURL := strings.TrimSpace(m[2])
		if linkURL == "" || strings.Contains(strings.ToLower(linkURL), "llms-full.txt") {
			continue
		}
		out = append(out, indexCandidate{
			Title: strings.TrimSpace(m[1]),
			URL:   linkURL,
			Notes: strings.TrimSpace(m[3]),
		})
	}
	return out
}

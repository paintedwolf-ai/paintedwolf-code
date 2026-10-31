package webresearch

import (
	"context"
	"golang.org/x/net/html"
	"net/url"
	"regexp"
	"strings"
)

func seedHostsSet(seeds []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, seed := range seeds {
		if h := urlHost(normalizePageURL(seed)); h != "" {
			out[strings.ToLower(h)] = struct{}{}
		}
		if h := urlHost(normalizeSiteBase(seed)); h != "" {
			out[strings.ToLower(h)] = struct{}{}
		}
	}
	return out
}

func pageSeedURL(raw string) string {
	normalized := normalizeSeed(raw)
	if normalized == "" {
		return ""
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	if strings.TrimSuffix(u.Path, "/") == "" {
		return ""
	}
	return normalized
}

func normalizePageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func normalizeSiteBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	path := strings.ToLower(u.Path)
	if strings.HasSuffix(path, "/llms.txt") || path == "/llms.txt" {
		u.Path = ""
	}
	u.Fragment = ""
	u.RawQuery = ""
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path != "" && u.Path != "/" {
		// Deep paths are usually pages, not site roots — use host only for index discovery.
		u.Path = ""
	}
	return strings.TrimRight(u.String(), "/")
}

func resolveMaybeRelativeURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

func sitemapTitle(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return u.Host
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		path = path[idx+1:]
	}
	return strings.ReplaceAll(path, "-", " ")
}

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

package webresearch

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"golang.org/x/net/html"
)

const (
	fetchBodyByteLimit  = 5 << 20
	defaultFetchTimeout = 30 * time.Second
	fetchUserAgent      = "painted-wolf-code-fetch/1.0 (+https://github.com/paintedwolf-ai/paintedwolf-code)"
)

// FetchOptions configures fetch_url.
type FetchOptions struct {
	URL     string
	Timeout time.Duration
	Mode    string // text (default) or raw — affects Accept header only for FetchURL/FetchRaw
}

// FetchURLResult carries the extracted body for caching and paging.
// Markdown marks HTML conversion so outlines can use headings.
type FetchURLResult struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	Markdown    bool   `json:"markdown,omitempty"`
}

// FetchRawResult is the verbatim body for mode=raw (no HTML→markdown).
type FetchRawResult struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
}

// FetchBodyTooLargeError reports a response beyond the body limit.
// Oversized responses produce no partial body.
type FetchBodyTooLargeError struct {
	Limit  int
	Actual int64
}

func (e FetchBodyTooLargeError) Error() string {
	return fmt.Sprintf("fetch response exceeds %d-byte limit", e.Limit)
}

// forgeRule maps a code-host "view" URL to its chrome-free raw equivalent.
type forgeRule struct {
	hosts   []string                // request hosts this rule applies to (lowercased)
	rewrite func(u *url.URL) string // returns the raw URL, or "" if the path doesn't match
}

// forgeRawRules maps repository view URLs to raw content URLs.
var forgeRawRules = []forgeRule{
	{hosts: []string{"github.com", "www.github.com"}, rewrite: segmentRewrite("raw.githubusercontent.com", "", "blob", "")},
	{hosts: []string{"gitlab.com"}, rewrite: segmentRewrite("", "-", "blob", "raw")},
	{hosts: []string{"bitbucket.org"}, rewrite: segmentRewrite("", "", "src", "raw")},
	{hosts: []string{"gist.github.com"}, rewrite: appendSuffix("raw", 2)},
}

// CanonicalFetchURL rewrites supported code views to raw file URLs.
func CanonicalFetchURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(u.Host)
	for _, rule := range forgeRawRules {
		for _, h := range rule.hosts {
			if h != host {
				continue
			}
			if raw := rule.rewrite(u); raw != "" {
				return raw
			}
		}
	}
	return rawURL
}

// segmentRewrite replaces a path marker after its required separator.
func segmentRewrite(rawHost, sep, view, raw string) func(*url.URL) string {
	return func(u *url.URL) string {
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		idx := -1
		for i, s := range segs {
			if s != view {
				continue
			}
			if sep != "" {
				if i == 0 || segs[i-1] != sep {
					continue
				}
			} else if i < 2 {
				continue
			}
			if i+1 >= len(segs) { // need at least a ref after the marker
				continue
			}
			idx = i
			break
		}
		if idx == -1 {
			return ""
		}
		out := append([]string{}, segs[:idx]...)
		if raw != "" {
			out = append(out, raw)
		}
		out = append(out, segs[idx+1:]...)
		host := u.Host
		if rawHost != "" {
			host = rawHost
		}
		return "https://" + host + "/" + strings.Join(out, "/")
	}
}

// appendSuffix adds a missing suffix to paths with enough segments.
func appendSuffix(suffix string, minSegs int) func(*url.URL) string {
	return func(u *url.URL) string {
		p := strings.Trim(u.Path, "/")
		if p == "" {
			return ""
		}
		segs := strings.Split(p, "/")
		if len(segs) < minSegs || segs[len(segs)-1] == suffix {
			return ""
		}
		return "https://" + u.Host + "/" + p + "/" + suffix
	}
}

// FetchURL pins public-IP requests and revalidates redirects before extracting readable text.
func FetchURL(ctx context.Context, opts FetchOptions) (FetchURLResult, error) {
	raw, err := fetchBytes(ctx, opts, "text")
	if err != nil {
		return FetchURLResult{}, err
	}
	return fetchTextResult(raw), nil
}

// fetchTextResult converts a fetched body to its text view. Image bodies never
// pass through here; their bytes are not text.
func fetchTextResult(raw FetchRawResult) FetchURLResult {
	text := string(raw.Body)
	title := ""
	markdown := false
	if !isImageMIME(raw.ContentType) && (strings.Contains(strings.ToLower(raw.ContentType), "text/html") || strings.HasPrefix(strings.TrimSpace(text), "<")) {
		title, text = extractHTMLText(text, raw.URL)
		markdown = true
	} else {
		text, _ = expandCompactJSON(raw.ContentType, text)
	}
	res := FetchURLResult{
		URL:         raw.URL,
		Status:      raw.Status,
		ContentType: raw.ContentType,
		Title:       title,
		Text:        text,
		Markdown:    markdown,
	}
	// Nonce markers are added at message publication, outside cached content.
	return sanitizeFetchResult(res)
}

// FetchRaw downloads a URL and returns the verbatim body (no HTML extraction).
func FetchRaw(ctx context.Context, opts FetchOptions) (FetchRawResult, error) {
	return fetchBytes(ctx, opts, "raw")
}

func fetchBytes(ctx context.Context, opts FetchOptions, mode string) (FetchRawResult, error) {
	u, err := normalizeFetchURL(opts.URL)
	if err != nil {
		return FetchRawResult{}, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	screenedURL, err := screenOutbound(ctx, secretmatch.SurfaceFetchURL, httpDestination(u), u.String())
	if err != nil {
		return FetchRawResult{}, err
	}
	if screenedURL != u.String() {
		u, err = normalizeFetchURL(screenedURL)
		if err != nil {
			return FetchRawResult{}, err
		}
	}
	resp, err := outboundhttp.Do(ctx, outboundhttp.Request{
		Method: "GET", URL: u.String(), Timeout: timeout, Redirects: outboundhttp.RedirectSafe,
		RedirectLimit: maxFetchRedirects, MaxBodyBytes: fetchBodyByteLimit,
		HopHeaders:   []outboundhttp.Header{{Name: "User-Agent", Value: fetchUserAgent}, {Name: "Accept", Value: acceptHeaderForMode(mode)}},
		AllowAddress: func(addr netip.Addr, _ uint16) bool { return ipAllowed(addr) },
		BeforeHop: func(hopCtx context.Context, hopURL *url.URL) error {
			return egressgate.AwaitHTTPRequest(hopCtx, hopURL, "GET")
		},
	})
	if err != nil {
		var tooLarge *outboundhttp.BodyTooLargeError
		if errors.As(err, &tooLarge) {
			return FetchRawResult{}, FetchBodyTooLargeError{Limit: fetchBodyByteLimit, Actual: int64(fetchBodyByteLimit + 1)}
		}
		return FetchRawResult{}, err
	}
	return FetchRawResult{
		URL:         resp.FinalURL,
		Status:      resp.Status,
		ContentType: resp.ContentType,
		Body:        resp.Body,
	}, nil
}

var blankLineRun = regexp.MustCompile(`\n{3,}`)

// Recovered titles are bounded before indexing or prompt inclusion.
const titleMaxChars = 800

// documentTitle reads the head title from the same parsed tree as the body.
func documentTitle(doc *html.Node) string {
	head := findElement(doc, "head")
	if head == nil {
		return ""
	}
	title := findElement(head, "title")
	if title == nil {
		return ""
	}
	var b strings.Builder
	for n := title.FirstChild; n != nil; n = n.NextSibling {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
	}
	return clampRunes(strings.TrimSpace(b.String()), titleMaxChars)
}

// findElement returns the first element with the given tag in document order.
func findElement(root *html.Node, tag string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && root.Data == tag {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func clampRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// Short article extractions fall back to the full document.
const readabilityMinChars = 200

// extractHTMLText renders the article as markdown, using the full document when extraction is thin.
func extractHTMLText(raw, pageURL string) (title, text string) {
	// Title and body share the same parsed document.
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", fetchHTMLExtractStub
	}
	title = documentTitle(doc)

	u, urlErr := url.Parse(pageURL)
	if urlErr == nil {
		if art, readErr := readability.FromReader(strings.NewReader(raw), u); readErr == nil && art.Node != nil {
			var b strings.Builder
			renderMarkdown(&b, art.Node, u)
			if cleaned := tidyMarkdown(b.String()); len(cleaned) >= readabilityMinChars {
				return title, cleaned
			}
		}
	}
	var b strings.Builder
	if urlErr != nil {
		u = nil
	}
	renderMarkdown(&b, doc, u)
	if full := tidyMarkdown(b.String()); full != "" {
		return title, full
	}
	return title, fetchHTMLExtractStub
}

// Failed extraction uses a bounded placeholder in output and cache.
const fetchHTMLExtractStub = "[fetch_url: could not extract readable text from HTML response]"

func tidyMarkdown(s string) string {
	return strings.TrimSpace(blankLineRun.ReplaceAllString(s, "\n\n"))
}

// renderMarkdown walks the HTML tree emitting markdown block structure.
// Skips comments and structurally hidden subtrees.
func renderMarkdown(b *strings.Builder, n *html.Node, base *url.URL) {
	if n.Type == html.CommentNode {
		return
	}
	if n.Type == html.ElementNode {
		if isHiddenElement(n) {
			return
		}
		switch n.Data {
		case "script", "style", "noscript", "head", "svg", "nav":
			return
		case "h1", "h2", "h3", "h4", "h5", "h6":
			if txt := inlineText(n); txt != "" {
				fmt.Fprintf(b, "\n%s %s\n\n", strings.Repeat("#", int(n.Data[1]-'0')), txt)
			}
			return
		case "pre":
			if txt := strings.TrimRight(rawText(n), "\n"); strings.TrimSpace(txt) != "" {
				fmt.Fprintf(b, "\n```\n%s\n```\n\n", txt)
			}
			return
		case "li":
			var item strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderMarkdown(&item, c, base)
			}
			if text := strings.TrimSpace(item.String()); text != "" {
				fmt.Fprintf(b, "- %s\n", text)
			}
			return
		case "a":
			text := inlineText(n)
			if text == "" {
				return
			}
			if href := markdownLinkURL(base, n); href != "" {
				fmt.Fprintf(b, "[%s](<%s>) ", escapeMarkdownLinkText(text), href)
			} else {
				b.WriteString(text)
				b.WriteString(" ")
			}
			return
		case "br":
			b.WriteString("\n")
			return
		case "p", "div", "section", "article", "blockquote", "tr", "figcaption", "td", "th", "ul", "ol":
			start := b.Len()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderMarkdown(b, c, base)
			}
			if b.Len() > start {
				b.WriteString("\n\n")
			}
			return
		}
	}
	if n.Type == html.TextNode {
		if t := strings.TrimSpace(n.Data); t != "" {
			b.WriteString(t)
			b.WriteString(" ")
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderMarkdown(b, c, base)
	}
}

func markdownLinkURL(base *url.URL, n *html.Node) string {
	if base == nil || isPermalinkAnchor(n) {
		return ""
	}
	var href string
	for _, attr := range n.Attr {
		if attr.Key == "href" {
			href = strings.TrimSpace(attr.Val)
			break
		}
	}
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if u.User != nil || u.Hostname() == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return ""
	}
	return u.String()
}

func escapeMarkdownLinkText(text string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return replacer.Replace(text)
}

// inlineText skips heading permalinks while collecting visible text.
func inlineText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.CommentNode {
			return
		}
		if x.Type == html.ElementNode {
			if isHiddenElement(x) {
				return
			}
			if x.Data == "script" || x.Data == "style" || x.Data == "noscript" {
				return
			}
		}
		if isPermalinkAnchor(x) {
			return
		}
		if x.Type == html.TextNode {
			if t := strings.TrimSpace(x.Data); t != "" {
				parts = append(parts, t)
			}
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, " ")
}

// permalinkClasses / permalinkPhrases match the self-link anchors doc generators
// add inside headings.
var (
	permalinkClasses = []string{"anchor", "headerlink", "header-anchor", "hash-link", "heading-link", "permalink"}
	permalinkPhrases = []string{"permalink", "anchor", "jump to", "link to"}
)

// isPermalinkAnchor identifies heading self-links whose text is navigation.
func isPermalinkAnchor(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Data != "a" {
		return false
	}
	var href, class, label string
	for _, a := range n.Attr {
		switch a.Key {
		case "href":
			href = a.Val
		case "class":
			class = strings.ToLower(a.Val)
		case "aria-label", "title":
			label += " " + strings.ToLower(a.Val)
		}
	}
	if !strings.HasPrefix(href, "#") {
		return false
	}
	for _, kw := range permalinkClasses {
		if strings.Contains(class, kw) {
			return true
		}
	}
	for _, kw := range permalinkPhrases {
		if strings.Contains(label, kw) {
			return true
		}
	}
	txt := strings.ToLower(strings.TrimSpace(rawText(n)))
	switch txt {
	case "", "¶", "§", "#", "🔗", "🔗︎":
		return true // icon-only or glyph permalink
	}
	return strings.Contains(txt, "permalink") || strings.Contains(txt, "jump to heading")
}

// rawText concatenates descendant text preserving whitespace (for <pre>).
func rawText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.CommentNode {
			return
		}
		if x.Type == html.ElementNode && isHiddenElement(x) {
			return
		}
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

package webindex

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/lycaon/lycaon/internal/textguard"
)

var htmlTagFast = regexp.MustCompile(`(?is)<[^>]*>`)

// looksLikeMarkup reports whether s likely contains HTML tags (cheap gate so
// plain API snippets skip html.Parse).
func looksLikeMarkup(s string) bool {
	i := strings.IndexByte(s, '<')
	if i < 0 || i+1 >= len(s) {
		return false
	}
	c := s[i+1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '/' || c == '!'
}

func htmlVisibleText(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return htmlTagFast.ReplaceAllString(s, " ")
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "head", "svg":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

func stripInvisibleAndControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' {
			b.WriteRune(' ')
			continue
		}
		if textguard.InvisibleFormatRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeWebTextForStorage turns untrusted web HTML/snippets into plain text
// suitable for web-index.db FTS fields (title, description, anchors). It is not
// applied to successful agent-facing fetch_url bodies.
func NormalizeWebTextForStorage(s string, maxLen int) string {
	prev := ""
	cur := s
	// Repeat until stable so stripping invisible / tags cannot reveal a second markup pass.
	for i := 0; i < 4 && cur != prev; i++ {
		prev = cur
		if looksLikeMarkup(cur) {
			cur = htmlVisibleText(cur)
		} else if strings.Contains(cur, "<") {
			cur = htmlTagFast.ReplaceAllString(cur, " ")
		}
		cur = html.UnescapeString(cur)
		cur = stripInvisibleAndControls(cur)
		cur = strings.Join(strings.Fields(cur), " ")
	}
	if maxLen > 0 && utf8.RuneCountInString(cur) > maxLen {
		runes := []rune(cur)
		cur = strings.TrimSpace(string(runes[:maxLen]))
	}
	return cur
}

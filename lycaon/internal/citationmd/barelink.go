package citationmd

import (
	"regexp"
	"strings"
)

// bareLinkPrefixes open a bare URL under the frontend's GFM rule, which is what
// decides whether text renders as a live link. It applies anywhere in text, even
// mid-word.
var bareLinkPrefixes = []string{"https://", "http://", "ftp://", "www."}

// bareLinkAt reads a bare URL at the start of rest, or returns "". tailEnd is the
// offset in rest where its run of URL bytes ends, supplied by the caller so that a
// long run is measured once.
func bareLinkAt(rest string, tailEnd func() int) string {
	switch rest[0] {
	case 'h', 'H', 'f', 'F', 'w', 'W':
	default:
		return ""
	}
	for _, prefix := range bareLinkPrefixes {
		if len(rest) > len(prefix) && strings.EqualFold(rest[:len(prefix)], prefix) && isHostByte(rest[len(prefix)]) {
			return backpedalURL(rest[:min(tailEnd(), MaxInlineProtocolNodeBytes)])
		}
	}
	return ""
}

func isHostByte(b byte) bool {
	return b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// backpedalURL drops what the rule treats as trailing punctuation: anything past
// an unclosed parenthesis, a trailing entity, and trailing sentence marks, except
// the closer of a balanced pair.
func backpedalURL(url string) string {
	var balanced map[int]bool
	end := len(url)
	for i := 0; i < end; i++ {
		if url[i] != '(' {
			continue
		}
		closer := strings.IndexByte(url[i:], ')')
		if closer < 0 {
			end = i
			break
		}
		i += closer
		if balanced == nil {
			balanced = map[int]bool{}
		}
		balanced[i] = true
	}
	url = url[:end]
	for len(url) > 0 {
		if trimmed, ok := trimTrailingEntity(url); ok {
			url = trimmed
			continue
		}
		last := len(url) - 1
		if !strings.ContainsRune(`?!.,:;*_'"~)`, rune(url[last])) || balanced[last] {
			return url
		}
		url = url[:last]
	}
	return url
}

func trimTrailingEntity(url string) (string, bool) {
	if url[len(url)-1] != ';' {
		return url, false
	}
	at := len(url) - 2
	for at >= 0 && isWordASCII(url[at]) {
		at--
	}
	if at >= 0 && url[at] == '&' && at < len(url)-2 {
		return url[:at], true
	}
	return url, false
}

func isWordASCII(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// bareLinkDestination is where a bare URL's link goes.
func bareLinkDestination(url string) string {
	if strings.EqualFold(url[:min(len(url), 4)], "www.") {
		return "http://" + url
	}
	return url
}

// htmlAnchor finds raw HTML anchors with an HTTP(S) href in a raw zone, skipping
// comments, which render nothing. It returns the spans of the opening tags.
func htmlAnchors(source string, zone Span) []Span {
	var out []Span
	for at := zone.Start; at < zone.End; {
		next := strings.IndexByte(source[at:zone.End], '<')
		if next < 0 {
			break
		}
		at += next
		rest := source[at:zone.End]
		if strings.HasPrefix(rest, "<!--") {
			closer := strings.Index(rest, "-->")
			if closer < 0 {
				break
			}
			at += closer + 3
			continue
		}
		closer := strings.IndexByte(rest, '>')
		if closer < 0 {
			break
		}
		if htmlAnchorHref(rest[:closer+1]) != "" {
			out = append(out, Span{at, at + closer + 1})
		}
		at += closer + 1
	}
	return out
}

var hrefAttribute = regexp.MustCompile(`(?i)^<a\s(?:.*\s)?href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// htmlAnchorHref reads an HTTP(S) href from an opening tag, or returns "".
func htmlAnchorHref(tag string) string {
	if len(tag) > MaxInlineProtocolNodeBytes {
		return ""
	}
	match := hrefAttribute.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	href := strings.TrimSpace(match[1] + match[2] + match[3])
	if !isWebDestination(href) {
		return ""
	}
	return href
}

package citationmd

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// bareLinkPrefixes open a bare URL under the frontend's GFM rule, which is what
// decides whether text renders as a live link. It applies anywhere in text, even
// mid-word.
var bareLinkPrefixes = []string{"https://", "http://", "ftp://", "www."}

// bareLinkRun finds a bare URL opening at the start of rest and returns the extent
// of its run of URL bytes, or zero. tailEnd supplies that extent so that a long run
// is measured once.
func bareLinkRun(rest string, tailEnd func() int) int {
	switch rest[0] {
	case 'h', 'H', 'f', 'F', 'w', 'W':
	default:
		return 0
	}
	for _, prefix := range bareLinkPrefixes {
		if len(rest) > len(prefix) && strings.EqualFold(rest[:len(prefix)], prefix) && isHostByte(rest[len(prefix)]) {
			return tailEnd()
		}
	}
	return 0
}

// urlRunEnd is where a URL's bytes stop: at `<` or at whitespace as the frontend's
// parser counts it, which includes Unicode spaces such as U+3000 and U+00A0.
func urlRunEnd(text string) int {
	for i := 0; i < len(text); {
		b := text[i]
		if b < utf8.RuneSelf {
			if b == '<' || b == ' ' || (b >= '\t' && b <= '\r') {
				return i
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == 0xFEFF || unicode.IsSpace(r) {
			return i
		}
		i += size
	}
	return len(text)
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

var (
	anchorOpen = regexp.MustCompile(`(?i)^</?(?:a|area)(?:[^a-zA-Z0-9:-]|$)`)
	wwwText    = regexp.MustCompile(`(?i)www\.`)
)

// neutralizeURLs keeps a URL's text visible with nothing the frontend can link.
func neutralizeURLs(text string) string {
	text = strings.ReplaceAll(text, "://", "&#58;//")
	return wwwText.ReplaceAllStringFunc(text, func(match string) string { return match[:3] + "&#46;" })
}

// neutralizeAnchors escapes raw HTML anchor tags, in every case and attribute form,
// so that only Markdown link forms can render live. Raw anchors are not a citation
// form; their text stays and claims nothing. protected spans of out are code or
// host-written links and stay byte for byte.
func neutralizeAnchors(out []byte, protected []Span) string {
	var b strings.Builder
	text := string(out)
	at, next := 0, 0
	for i := 0; i < len(text); {
		for next < len(protected) && protected[next].End <= i {
			next++
		}
		if next < len(protected) && protected[next].Start <= i {
			i = protected[next].End
			continue
		}
		offset := strings.IndexByte(text[i:], '<')
		if offset < 0 {
			break
		}
		i += offset
		if next < len(protected) && protected[next].Start <= i {
			continue
		}
		window := text[i:min(len(text), i+8)]
		if !anchorOpen.MatchString(window) {
			i++
			continue
		}
		end := tagEnd(text, i)
		b.WriteString(text[at:i])
		b.WriteString(escapeTag(text[i:end]))
		at, i = end, end
	}
	if at == 0 {
		return text
	}
	b.WriteString(text[at:])
	return b.String()
}

// tagEnd finds the end of a tag with quoted attributes; an unclosed one runs to the
// end of its line, bounded by the node cap.
func tagEnd(text string, at int) int {
	limit := min(len(text), at+MaxInlineProtocolNodeBytes)
	var quote byte
	for i := at + 1; i < limit; i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1
		}
	}
	if newline := strings.IndexByte(text[at:limit], '\n'); newline >= 0 {
		return at + newline
	}
	return limit
}

var tagEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;", ":", "&#58;", ".", "&#46;")

func escapeTag(tag string) string { return tagEscaper.Replace(tag) }

// escapedDocument shows a whole source as one code block, which creates no links.
func escapedDocument(source string) string {
	longest, run := 0, 0
	for i := 0; i < len(source); i++ {
		if source[i] == '`' {
			if run++; run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "\n" + source + "\n" + fence
}

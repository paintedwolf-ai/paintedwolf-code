package governance

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// mdSectionPreviewBytes bounds the body preview kept per heading in a
// degraded digest.
const mdSectionPreviewBytes = 160

// mdSection is one Markdown heading paired with a short preview of the text
// immediately under it.
type mdSection struct {
	heading string
	preview string
}

func (s mdSection) render() string {
	if s.preview == "" {
		return s.heading + "\n"
	}
	return s.heading + "\n  " + s.preview + "\n"
}

// degradeOversizedMarkdown builds a heading digest for a body over maxBytes:
// whole sections, heading plus a short preview, in document order until the
// budget runs out, with a count of what got dropped. ok is false when the
// body has no headings, or the budget can't hold even one section.
func degradeOversizedMarkdown(content string, maxBytes int, path string) (string, bool) {
	sections := extractMarkdownSections(content)
	if len(sections) == 0 {
		return "", false
	}

	path = strings.TrimSpace(path)
	if path == "" {
		path = "this file"
	}
	header := fmt.Sprintf(
		"This body exceeds the %d-byte inject safety valve; showing a structural outline instead of the full text. Read `%s` directly for the complete policy.\n\n",
		maxBytes, path,
	)
	marker := AgentsMDBodyTruncatedMarker
	// Sized as if every section were omitted, so the count itself always fits.
	footerBudget := len(omissionFooter(len(sections), path))
	budget := maxBytes - len(header) - len(marker) - footerBudget
	if budget <= 0 {
		return "", false
	}

	var body strings.Builder
	included := 0
	for _, sec := range sections {
		line := sec.render()
		if body.Len()+len(line) > budget {
			break
		}
		body.WriteString(line)
		included++
	}
	if included == 0 {
		return "", false
	}

	if omitted := len(sections) - included; omitted > 0 {
		body.WriteString(omissionFooter(omitted, path))
	}
	return header + body.String() + marker, true
}

func omissionFooter(omitted int, path string) string {
	return fmt.Sprintf("\n… +%d more section(s) not shown; read `%s` directly for the rest.\n", omitted, path)
}

// truncateRuneSafe trims s to at most n bytes without splitting a UTF-8 rune.
func truncateRuneSafe(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// extractMarkdownSections scans ATX headings (`#` through `######`) and
// pairs each with a short preview of its first line(s) of body text.
func extractMarkdownSections(content string) []mdSection {
	var sections []mdSection
	var cur *mdSection
	previewBudget := 0
	for _, line := range strings.Split(content, "\n") {
		if title, ok := parseATXHeading(line); ok {
			sections = append(sections, mdSection{heading: title})
			cur = &sections[len(sections)-1]
			previewBudget = mdSectionPreviewBytes
			continue
		}
		if cur == nil || previewBudget <= 0 {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > previewBudget {
			trimmed = truncateRuneSafe(trimmed, previewBudget)
		}
		if cur.preview != "" {
			trimmed = " " + trimmed
		}
		cur.preview += trimmed
		previewBudget -= len(trimmed)
	}
	return sections
}

// parseATXHeading matches `#` through `######` (up to 3 leading spaces, then
// a space or end of line) and returns the full heading line. A bare `#word`
// with no separating space does not match.
func parseATXHeading(line string) (headingLine string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return "", false
	}
	rest := trimmed[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimRight(rest, " \t#"))
	return trimmed[:level] + " " + title, true
}

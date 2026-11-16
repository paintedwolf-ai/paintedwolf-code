package reproducedsource

import (
	"strings"
	"unicode"
)

func splitFencedZone(raw string) (lang string, interior []string) {
	lines := splitRawLines(raw)
	if len(lines) == 0 {
		return "", nil
	}
	opener := lines[0]
	fenceChar, fenceRun, quoteDepth, info := parseFenceOpener(opener)
	fields := strings.Fields(info)
	if len(fields) > 0 {
		lang = fields[0]
	}
	if len(lines) == 1 {
		return lang, nil
	}
	body := lines[1:]
	if len(body) > 0 {
		last := stripBlockquotePrefixes(body[len(body)-1], quoteDepth)
		if isClosingFenceLine(last, fenceChar, fenceRun) {
			body = body[:len(body)-1]
		}
	}
	if quoteDepth > 0 {
		stripped := make([]string, len(body))
		for i, line := range body {
			stripped[i] = stripBlockquotePrefixes(line, quoteDepth)
		}
		body = stripped
	}
	return lang, body
}

func parseFenceOpener(line string) (fenceChar byte, fenceRun int, quoteDepth int, info string) {
	at := 0
	for at < len(line) && line[at] != '`' && line[at] != '~' {
		if line[at] == '>' {
			quoteDepth++
		}
		at++
	}
	if at >= len(line) {
		return '`', 3, quoteDepth, ""
	}
	fenceChar = line[at]
	end := at
	for end < len(line) && line[end] == fenceChar {
		end++
	}
	return fenceChar, end - at, quoteDepth, strings.TrimSpace(line[end:])
}

func stripBlockquotePrefixes(line string, depth int) string {
	s := line
	for range depth {
		at := 0
		for at < 3 && at < len(s) && s[at] == ' ' {
			at++
		}
		if at >= len(s) || s[at] != '>' {
			break
		}
		at++
		if at < len(s) && (s[at] == ' ' || s[at] == '\t') {
			at++
		}
		s = s[at:]
	}
	return s
}

func isClosingFenceLine(line string, fenceChar byte, minRun int) bool {
	s := strings.TrimLeft(line, " \t")
	run := 0
	for run < len(s) && s[run] == fenceChar {
		run++
	}
	if run < minRun {
		return false
	}
	return strings.TrimSpace(s[run:]) == ""
}

func splitIndentedZone(raw string) []string {
	lines := splitRawLines(raw)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = stripCodeBlockIndent(line)
	}
	return out
}

func stripCodeBlockIndent(line string) string {
	cols := 0
	at := 0
	for at < len(line) && cols < 4 {
		switch line[at] {
		case ' ':
			cols++
			at++
		case '\t':
			cols = 4
			at++
		default:
			return line[at:]
		}
	}
	return line[at:]
}

func splitRawLines(raw string) []string {
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimSuffix(raw, "\n")
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func normalizeFenceLines(interior []string) ([]normLine, map[string][]int) {
	if len(interior) == 0 {
		return nil, nil
	}
	working := interior
	if allCatNNumbered(interior) {
		stripped := make([]string, len(interior))
		for i, line := range interior {
			if rest, ok := stripCatNPrefix(line); ok {
				stripped[i] = rest
			} else {
				stripped[i] = line
			}
		}
		working = stripped
	}

	var out []normLine
	var pos map[string][]int
	for i, raw := range working {
		norm := strings.TrimSpace(raw)
		if norm == "" {
			continue
		}
		sub := hasMinNonSpaceRunes(norm, minSubstantiveRunes)
		idx := len(out)
		out = append(out, normLine{
			lineNum:     i + 1,
			text:        norm,
			substantive: sub,
		})
		if sub {
			if pos == nil {
				pos = make(map[string][]int)
			}
			pos[norm] = append(pos[norm], idx)
		}
	}
	return out, pos
}

func allCatNNumbered(lines []string) bool {
	sawNonBlank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, ok := stripCatNPrefix(line); !ok {
			return false
		}
		sawNonBlank = true
	}
	return sawNonBlank
}

func stripCatNPrefix(line string) (string, bool) {
	at := 0
	for at < len(line) && line[at] == ' ' {
		at++
	}
	digitsStart := at
	for at < len(line) && line[at] >= '0' && line[at] <= '9' {
		at++
	}
	if at == digitsStart || at >= len(line) || line[at] != '\t' {
		return "", false
	}
	return line[at+1:], true
}

func normalizeFileLines(content string) []normLine {
	if content == "" {
		return nil
	}
	rawLines := strings.Split(content, "\n")
	out := make([]normLine, 0, len(rawLines))
	for i, raw := range rawLines {
		norm := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if norm == "" {
			continue
		}
		out = append(out, normLine{
			lineNum:     i + 1,
			text:        norm,
			substantive: hasMinNonSpaceRunes(norm, minSubstantiveRunes),
		})
	}
	return out
}

func hasMinNonSpaceRunes(s string, want int) bool {
	count := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			count++
			if count >= want {
				return true
			}
		}
	}
	return false
}

func atLineStart(source string, at int) bool {
	lineStart := strings.LastIndexByte(source[:at], '\n') + 1
	prefix := source[lineStart:at]
	return len(prefix) <= 3 && strings.TrimSpace(prefix) == ""
}

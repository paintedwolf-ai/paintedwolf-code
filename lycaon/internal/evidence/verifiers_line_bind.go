package evidence

import (
	"fmt"
	"strings"
)

func effectiveClaimPath(rec Record, claimPath string) string {
	claimPath = NormalizeLedgerPath(claimPath)
	if claimPath != "" {
		return claimPath
	}
	return NormalizeLedgerPath(rec.Path)
}

func lineContentIndex(rec Record, claimPath string) map[int]string {
	claimPath = effectiveClaimPath(rec, claimPath)
	idx := map[int]string{}
	for _, body := range rec.Body {
		for _, line := range strings.Split(body, "\n") {
			m := readBodyLinePrefixRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			n, err := parseLineNumber(m[1])
			if err != nil || n <= 0 {
				continue
			}
			idx[n] = m[3]
		}
	}
	if rec.grepLines != nil && claimPath != "" {
		if byLine, ok := rec.grepLines[claimPath]; ok {
			for line, content := range byLine {
				if line > 0 {
					idx[line] = content
				}
			}
		}
	}
	if len(idx) == 0 {
		return nil
	}
	return idx
}

func parseLineNumber(raw string) (int, error) {
	var n int
	_, err := fmt.Sscanf(raw, "%d", &n)
	return n, err
}

func excerptLinesNormalized(excerpt string) []string {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(excerpt, "\n") {
		n := normalizeForExcerptMatch(line)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func excerptAtCitedLine(rec Record, claimPath string, line int, excerpt string, window int) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" || line <= 0 || !ExcerptMeaningful(excerpt) {
		return false
	}
	exLines := excerptLinesNormalized(excerpt)
	if len(exLines) == 0 {
		return false
	}
	idx := lineContentIndex(rec, claimPath)
	if idx == nil {
		return false
	}
	if window < 0 {
		window = 0
	}
	for anchor := line - window; anchor <= line+window; anchor++ {
		if anchor <= 0 {
			continue
		}
		if !excerptLinesMatchAtAnchor(idx, anchor, exLines) {
			continue
		}
		return true
	}
	return false
}

func excerptLinesMatchAtAnchor(idx map[int]string, anchor int, exLines []string) bool {
	for k, want := range exLines {
		line := anchor + k
		content, ok := idx[line]
		if !ok {
			return false
		}
		got := normalizeForExcerptMatch(content)
		if !strings.Contains(got, want) {
			return false
		}
	}
	return true
}

func FormatReadBodyForVerification(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" && strings.HasSuffix(content, "\n") {
		lines = lines[:n-1]
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d\t%s", i+1, line)
	}
	return b.String()
}

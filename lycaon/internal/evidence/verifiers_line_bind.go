package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
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
			n, text, ok := hostmarker.ParseNumberedLine(line)
			if !ok || n <= 0 {
				continue
			}
			idx[n] = text
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

// excerptLinesNormalized returns the excerpt's non-empty lines in match form.
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

func reanchorExcerpt(ev Ledger, path string, excerpt string) (int, string, bool) {
	excerpt = strings.TrimSpace(excerpt)
	if len(excerpt) < EvidenceMinMeaningfulSpan || path == "" || ev.ByPath == nil {
		return 0, "", false
	}
	exLines := excerptLinesNormalized(excerpt)
	if len(exLines) == 0 {
		return 0, "", false
	}
	handles := ev.ByPath[path]
	if len(handles) == 0 {
		return 0, "", false
	}
	matched := map[int]string{}
	for _, h := range handles {
		rec, ok := ResolveHandle(ev, h)
		if !ok || rec.SupersededBy != "" {
			continue
		}
		idx := lineContentIndex(rec, path)
		if idx == nil {
			continue
		}
		for line := range idx {
			if line > 0 && excerptLinesMatchAtAnchor(idx, line, exLines) {
				if existing, exists := matched[line]; !exists || h < existing {
					matched[line] = h
				}
			}
		}
	}
	if len(matched) == 1 {
		for line, h := range matched {
			return line, h, true
		}
	}
	return 0, "", false
}

// FormatReadBodyForVerification numbers a whole file from line one, as a read
// of that file would have shown it.
func FormatReadBodyForVerification(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" && strings.HasSuffix(content, "\n") {
		lines = lines[:n-1]
	}
	return hostmarker.FormatNumberedLines(lines, 1)
}

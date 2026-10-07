package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// normalizeForExcerptMatch drops rendered line numbers and whitespace layout,
// so an excerpt copied from a numbered read compares against the line text.
func normalizeForExcerptMatch(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if _, text, ok := hostmarker.ParseNumberedLine(line); ok {
			lines[i] = text
		}
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// ExcerptMeaningful reports whether an excerpt is long enough to identify a
// span; an absent excerpt makes no claim and passes.
func ExcerptMeaningful(excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return true
	}
	return len(excerpt) >= EvidenceMinMeaningfulSpan
}

func excerptInRecordBodies(rec Record, excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return true
	}
	if !ExcerptMeaningful(excerpt) {
		return false
	}
	needle := normalizeForExcerptMatch(excerpt)
	if needle == "" {
		return false
	}
	for _, body := range rec.Body {
		if strings.Contains(normalizeForExcerptMatch(body), needle) {
			return true
		}
	}
	if rec.grepLines != nil {
		for _, byLine := range rec.grepLines {
			for _, content := range byLine {
				if strings.Contains(normalizeForExcerptMatch(content), needle) {
					return true
				}
			}
		}
	}
	return false
}

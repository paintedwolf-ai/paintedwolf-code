package evidence

import (
	"regexp"
	"strings"
)

// readLineNumberPrefixRE matches rendered line-number prefixes.
var readLineNumberPrefixRE = regexp.MustCompile(`(?m)^\d+[\t|]`)

// readBodyLinePrefixRE parses one prefixed read/grep body line into line number and text.
var readBodyLinePrefixRE = regexp.MustCompile(`^(\d+)([\t|])(.*)$`)

// normalizeForExcerptMatch ignores line prefixes and whitespace layout.
func normalizeForExcerptMatch(s string) string {
	s = readLineNumberPrefixRE.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

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

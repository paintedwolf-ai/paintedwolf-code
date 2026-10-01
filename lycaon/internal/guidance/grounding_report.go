package guidance

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"strings"
)

const (
	// MaxOffenderDisplay caps tokens shown in hint template sample vars.
	MaxOffenderDisplay = 8
	// MaxOffenderSampleChars caps total sample string length for hint templates.
	MaxOffenderSampleChars = 240
)

// FormatOffenderReport builds bounded reject display fields from full offender tokens.
func FormatOffenderReport(tokens []string) evidence.OffenderReport {
	count := len(tokens)
	if count == 0 {
		return evidence.OffenderReport{}
	}
	display := make([]string, 0, MaxOffenderDisplay)
	remaining := MaxOffenderSampleChars
	for _, tok := range tokens {
		if len(display) >= MaxOffenderDisplay {
			break
		}
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		extra := len(tok)
		if len(display) > 0 {
			extra++ // comma separator
		}
		if len(display) > 0 && remaining-extra < 0 {
			break
		}
		if len(display) == 0 && extra > MaxOffenderSampleChars {
			if len(tok) > MaxOffenderSampleChars {
				// The cap is a byte offset, so it can land mid-rune.
				tok = strings.ToValidUTF8(tok[:MaxOffenderSampleChars], "")
			}
		}
		display = append(display, tok)
		remaining -= extra
	}
	omitted := count - len(display)
	if omitted < 0 {
		omitted = 0
	}
	return evidence.OffenderReport{
		Count:   count,
		Sample:  strings.Join(display, ", "),
		Omitted: omitted,
	}
}

func addOffender(out *[]string, seen map[string]struct{}, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if _, ok := seen[token]; ok {
		return
	}
	seen[token] = struct{}{}
	*out = append(*out, token)
}

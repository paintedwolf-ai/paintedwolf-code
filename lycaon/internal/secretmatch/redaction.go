package secretmatch

import (
	"sort"
	"strings"
)

// RedactionSource names the evidence that justified one replacement.
type RedactionSource string

const (
	// SourceShapeRule is a published catalog rule matching by pattern.
	SourceShapeRule RedactionSource = "shape_rule"
	// SourceContainerHarvest is membership of a credential-class container.
	SourceContainerHarvest RedactionSource = "container_harvest"
	// SourceRememberedMatch is a confirmed value matched by containment.
	SourceRememberedMatch RedactionSource = "remembered_match"
)

// RedactedMarker replaces bytes a screen removed.
const RedactedMarker = "[REDACTED]"

var redactedPlaceholder = []rune(RedactedMarker)

// PlaceholderRunes is the placeholder marker width.
func PlaceholderRunes() int { return len(redactedPlaceholder) }

// RedactionSpan locates one marker in the rewritten string.
type RedactionSpan struct {
	Start       int
	Length      int
	RuleID      string
	Title       string
	Source      RedactionSource
	Fingerprint SecretFingerprint
}

// redactionInterval is one covering range and the match that named it. A
// non-empty reference is written instead of the placeholder.
type redactionInterval struct {
	start, end int
	by         Match
	reference  string
}

// redactMatches merges overlaps and splits replacements at line breaks.
func redactMatches(s string, hits []Match) (string, []RedactionSpan) {
	out, written := rewriteMatches(s, hits, false)
	spans := make([]RedactionSpan, len(written))
	for i := range written {
		spans[i] = written[i].RedactionSpan
	}
	return out, spans
}

// rewriteMatches replaces every covered range. With references, live managed
// values are written as references and the rest of each range as placeholders.
func rewriteMatches(s string, hits []Match, references bool) (string, []Replacement) {
	if len(hits) == 0 {
		return s, nil
	}
	runes := []rune(s)
	merged := mergeIntervals(runes, hits)
	if references {
		carved := make([]redactionInterval, 0, len(merged))
		for _, iv := range merged {
			carved = append(carved, carveReferences(iv, hits)...)
		}
		merged = carved
	}
	merged = splitIntervalsOnLineBreaks(runes, merged)

	out := make([]rune, 0, len(runes))
	written := make([]Replacement, 0, len(merged))
	cursor := 0
	for _, iv := range merged {
		out = append(out, runes[cursor:iv.start]...)
		marker := redactedPlaceholder
		if iv.reference != "" {
			marker = []rune(iv.reference)
		}
		written = append(written, Replacement{
			RedactionSpan: RedactionSpan{
				Start:       len(out),
				Length:      len(marker),
				RuleID:      iv.by.RuleID,
				Title:       iv.by.Title,
				Source:      iv.by.Source,
				Fingerprint: iv.by.Fingerprint,
			},
			Reference:   iv.reference,
			SourceStart: iv.start,
			SourceEnd:   iv.end,
		})
		out = append(out, marker...)
		cursor = iv.end
	}
	return string(append(out, runes[cursor:]...)), written
}

// mergeIntervals unions overlapping in-bounds matches under the preferred one.
func mergeIntervals(runes []rune, hits []Match) []redactionInterval {
	ordered := make([]Match, len(hits))
	copy(ordered, hits)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	merged := make([]redactionInterval, 0, len(ordered))
	for _, span := range ordered {
		if span.End <= span.Start || span.Start < 0 || span.End > len(runes) {
			continue
		}
		if n := len(merged); n > 0 && span.Start < merged[n-1].end {
			if span.End > merged[n-1].end {
				merged[n-1].end = span.End
			}
			if prefer(span, merged[n-1].by) {
				merged[n-1].by = span
			}
			continue
		}
		merged = append(merged, redactionInterval{start: span.Start, end: span.End, by: span})
	}
	return merged
}

// splitIntervalsOnLineBreaks retains attribution on each non-empty segment.
// References name a whole value and are never split.
func splitIntervalsOnLineBreaks(runes []rune, intervals []redactionInterval) []redactionInterval {
	out := make([]redactionInterval, 0, len(intervals))
	for _, iv := range intervals {
		if iv.reference != "" {
			out = append(out, iv)
			continue
		}
		segment := iv.start
		for i := iv.start; i < iv.end; i++ {
			if runes[i] != '\n' && runes[i] != '\r' {
				continue
			}
			if i > segment {
				out = append(out, redactionInterval{start: segment, end: i, by: iv.by})
			}
			segment = i + 1
		}
		if iv.end > segment {
			out = append(out, redactionInterval{start: segment, end: iv.end, by: iv.by})
		}
	}
	return out
}

// pseudonymRunes bounds a marker's correlation tag.
const pseudonymRunes = 8

// PseudonymFor renders a stable tag from a device-keyed fingerprint.
func PseudonymFor(fp SecretFingerprint) string {
	body := strings.TrimPrefix(string(fp), fingerprintPrefix)
	if body == "" {
		return ""
	}
	if runes := []rune(body); len(runes) > pseudonymRunes {
		body = string(runes[:pseudonymRunes])
	}
	return fingerprintPrefix + body
}

// taggedMarker inserts a tag into the shared placeholder.
func taggedMarker(tag string) []rune {
	body := redactedPlaceholder[:len(redactedPlaceholder)-1]
	return append(append(append([]rune(nil), body...), ':'), append([]rune(tag), ']')...)
}

// ApplyPseudonyms widens markers back to front so offsets stay valid.
func ApplyPseudonyms(redacted string, spans []RedactionSpan) string {
	if len(spans) == 0 {
		return redacted
	}
	ordered := make([]RedactionSpan, len(spans))
	copy(ordered, spans)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start > ordered[j].Start })
	runes := []rune(redacted)
	for _, span := range ordered {
		tag := PseudonymFor(span.Fingerprint)
		if tag == "" || span.Start < 0 || span.Start+span.Length > len(runes) {
			continue
		}
		tail := append(taggedMarker(tag), runes[span.Start+span.Length:]...)
		runes = append(runes[:span.Start], tail...)
	}
	return string(runes)
}

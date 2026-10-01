package secretmatch

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// A reference token is the host's value-free handle for a managed secret. The
// host writes it wherever the value would appear, so it is host grammar, never
// evidence: detectors read it masked, and no redaction may rewrite one.
const referenceTokenPrefix = "{{paintedwolf-secret:"

var referenceTokenPattern = regexp.MustCompile(`\{\{paintedwolf-secret:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\}\}`)

// ReferenceToken returns the token for a managed secret id.
func ReferenceToken(id string) string {
	return referenceTokenPrefix + strings.TrimSpace(id) + "}}"
}

// ParseReferenceToken accepts only one complete token, ignoring surrounding space.
func ParseReferenceToken(token string) (string, bool) {
	token = strings.TrimSpace(token)
	match := referenceTokenPattern.FindStringSubmatch(token)
	if len(match) != 2 || match[0] != token {
		return "", false
	}
	return match[1], true
}

// ContainsReferenceToken reports a complete token anywhere in text.
func ContainsReferenceToken(text string) bool {
	return referenceTokenPattern.MatchString(text)
}

// ReferenceTokenIndexes returns each complete token's byte span followed by its
// id's byte span, as regexp submatch indexes.
func ReferenceTokenIndexes(text string) [][]int {
	return referenceTokenPattern.FindAllStringSubmatchIndex(text, -1)
}

// ReplaceReferenceTokens rewrites each complete token through replace, which
// receives the token's id.
func ReplaceReferenceTokens(text string, replace func(id string) string) string {
	return referenceTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		id, _ := ParseReferenceToken(token)
		return replace(id)
	})
}

// ContainsMalformedReferenceToken reports the token prefix at a position that
// does not begin a complete token, such as a token whose id was rewritten.
func ContainsMalformedReferenceToken(text string) bool {
	for offset := 0; ; {
		at := strings.Index(text[offset:], referenceTokenPrefix)
		if at < 0 {
			return false
		}
		start := offset + at
		span := referenceTokenPattern.FindStringIndex(text[start:])
		if len(span) < 2 || span[0] != 0 {
			return true
		}
		offset = start + span[1]
	}
}

// referenceTokenMaskByte replaces token bytes. It is neither whitespace, which
// a rule could skip to capture the text after a token, nor a character a
// credential alphabet includes.
const referenceTokenMaskByte = "*"

// referenceTokenMask is detector input with every complete token masked.
// Tokens are ASCII, so a one-byte mask keeps byte and rune offsets.
type referenceTokenMask struct {
	text string
	// spans are the masked rune ranges, adjacent tokens merged.
	spans []protectedSpan
}

func maskReferenceTokens(text string) referenceTokenMask {
	if !strings.Contains(text, referenceTokenPrefix) {
		return referenceTokenMask{text: text}
	}
	indexes := referenceTokenPattern.FindAllStringIndex(text, -1)
	if len(indexes) == 0 {
		return referenceTokenMask{text: text}
	}
	var masked strings.Builder
	masked.Grow(len(text))
	spans := make([]protectedSpan, 0, len(indexes))
	lastByte, lastRune := 0, 0
	for _, index := range indexes {
		masked.WriteString(text[lastByte:index[0]])
		masked.WriteString(strings.Repeat(referenceTokenMaskByte, index[1]-index[0]))
		start := lastRune + utf8.RuneCountInString(text[lastByte:index[0]])
		end := start + index[1] - index[0]
		lastByte, lastRune = index[1], end
		if n := len(spans); n > 0 && spans[n-1].end == start {
			spans[n-1].end = end
			continue
		}
		spans = append(spans, protectedSpan{start, end})
	}
	masked.WriteString(text[lastByte:])
	return referenceTokenMask{text: masked.String(), spans: spans}
}

// outside keeps the parts of each hit outside masked tokens, so a rule whose
// capture runs into a mask never rewrites a token's bytes.
func (mask referenceTokenMask) outside(hits []Match) []Match {
	if len(mask.spans) == 0 || len(hits) == 0 {
		return hits
	}
	out := make([]Match, 0, len(hits))
	for _, hit := range hits {
		start := hit.Start
		for _, span := range mask.spans {
			if span.end <= start || span.start >= hit.End {
				continue
			}
			if span.start > start {
				part := hit
				part.Start, part.End = start, span.start
				out = append(out, part)
			}
			start = max(start, span.end)
		}
		if start < hit.End {
			part := hit
			part.Start = start
			out = append(out, part)
		}
	}
	return out
}

// WithinReferenceTokens reports whether every occurrence of value in text lies
// inside complete tokens. File scanners, which cannot mask their input, use it
// to drop findings a token produced.
func WithinReferenceTokens(text, value string) bool {
	if value == "" || !strings.Contains(text, referenceTokenPrefix) {
		return false
	}
	indexes := referenceTokenPattern.FindAllStringIndex(text, -1)
	found := false
	for offset := 0; ; {
		at := strings.Index(text[offset:], value)
		if at < 0 {
			return found
		}
		start := offset + at
		if !bytesCoveredByTokens(indexes, start, start+len(value)) {
			return false
		}
		found = true
		offset = start + 1
	}
}

// bytesCoveredByTokens reports whether [start, end) lies inside contiguous tokens.
func bytesCoveredByTokens(indexes [][]int, start, end int) bool {
	for _, index := range indexes {
		if index[1] <= start {
			continue
		}
		if index[0] > start {
			return false
		}
		start = index[1]
		if start >= end {
			return true
		}
	}
	return false
}

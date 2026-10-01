package secretmatch

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ManagedValue carries host-known identity across serialization boundaries.
func (m *Matcher) ManagedValue(value, name, reference string) (Match, error) {
	if m == nil || m.fingerprinter == nil {
		return Match{}, fmt.Errorf("managed secret fingerprinting is unavailable")
	}
	return Match{
		RuleID: ManagedRuleID, Title: ManagedRuleTitle, Severity: "high",
		Fingerprint: m.fingerprinter.Fingerprint(value), VarName: name,
		Reference: reference, NonDisclosable: true, GenericShape: "Protected value",
		Source: SourceRememberedMatch,
	}, nil
}

// MergeEvidence retains one finding per identity for policy and approval reuse.
func MergeEvidence(groups ...[]Match) []Match {
	seen := map[SecretFingerprint]bool{}
	var out []Match
	for _, matches := range groups {
		for _, match := range matches {
			if match.Fingerprint != "" && seen[match.Fingerprint] {
				continue
			}
			seen[match.Fingerprint] = true
			out = append(out, match)
		}
	}
	return out
}

// OutsideProtected retains exact evidence and shapes extending beyond attributed spans.
// The caller screens source fields and supplies their original identities separately.
func OutsideProtected(text string, matches []Match, protected []string) []Match {
	if len(matches) == 0 || len(protected) == 0 {
		return matches
	}
	spans := protectedSpans(text, protected)
	var out []Match
	for _, match := range matches {
		// Exact evidence can identify a different credential containing this value.
		if match.Source != SourceShapeRule {
			out = append(out, match)
			continue
		}
		i := sort.Search(len(spans), func(i int) bool { return spans[i].end > match.Start })
		if i == len(spans) || spans[i].start > match.Start || spans[i].end < match.End {
			out = append(out, match)
		}
	}
	return out
}

type protectedSpan struct{ start, end int }

func protectedSpans(text string, protected []string) []protectedSpan {
	var spans []protectedSpan
	seen := map[string]bool{}
	for _, value := range protected {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		first := len(spans)
		for offset := 0; offset < len(text); {
			at := strings.Index(text[offset:], value)
			if at < 0 {
				break
			}
			start := offset + at
			end := start + len(value)
			if len(spans) > first && spans[len(spans)-1].end >= start {
				spans[len(spans)-1].end = end
			} else {
				spans = append(spans, protectedSpan{start, end})
			}
			offset = start + 1
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) > 0 && merged[len(merged)-1].end >= span.start {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	lastByte, lastRune := 0, 0
	for i, span := range merged {
		start := lastRune + utf8.RuneCountInString(text[lastByte:span.start])
		end := start + utf8.RuneCountInString(text[span.start:span.end])
		merged[i] = protectedSpan{start, end}
		lastByte, lastRune = span.end, end
	}
	return merged
}

// RedactEvidence applies already screened, value-relative spans.
func RedactEvidence(text string, matches []Match) string {
	out, _ := redactMatches(text, matches)
	return out
}

// ManagedNames is value-free presentation metadata from exact managed evidence.
func ManagedNames(matches []Match) []string {
	seen := map[string]bool{}
	var names []string
	for _, match := range matches {
		if !IsManagedRule(match.RuleID) || match.VarName == "" || seen[match.VarName] {
			continue
		}
		seen[match.VarName] = true
		names = append(names, match.VarName)
	}
	sort.Strings(names)
	return names
}

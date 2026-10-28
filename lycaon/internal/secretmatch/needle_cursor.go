package secretmatch

import (
	"context"
	"strings"
	"unicode/utf8"
)

type needleCursor struct {
	byteAt, occurrences int
	runeByteAt, runeAt  int
	ctx                 context.Context
}

// needleMatchesVisit is the exact-match policy shared by complete-field and
// incremental screening. stop admits candidate starts, not candidate ends.
func (m *Matcher) needleMatchesVisit(s string, n needle, v HarvestedValue, state *needleCursor, stop int, visit func(Match, int, int) error) error {
	if n.text == "" || len(n.text) > len(s) {
		state.byteAt = stop
		return nil
	}
	shape := ""
	for state.byteAt < len(s) && (v.NonDisclosable || state.occurrences < maxHarvestOccurrences) {
		if state.ctx != nil {
			if err := state.ctx.Err(); err != nil {
				return err
			}
		}
		rel := strings.Index(s[state.byteAt:], n.text)
		if rel < 0 {
			state.byteAt = stop
			break
		}
		at := state.byteAt + rel
		if at >= stop {
			state.byteAt = stop
			break
		}
		state.byteAt = at + len(n.text)
		if !v.NonDisclosable && wordBounded(s, at, state.byteAt) {
			continue
		}
		state.occurrences++
		start := state.runeAt + utf8.RuneCountInString(s[state.runeByteAt:at])
		end := start + utf8.RuneCountInString(n.text)
		state.runeAt = end
		state.runeByteAt = state.byteAt
		if shape == "" {
			shape = GenericShape(n.plain)
		}
		reference := ""
		if n.whole {
			reference = v.Reference
		}
		hit := Match{RuleID: v.RuleID, Title: v.Title, Severity: "high", Start: start, End: end, GenericShape: shape, Fingerprint: v.Fingerprint, VarName: v.Name, Container: v.Container, Source: v.Source, Reference: reference, Retired: v.Retired, NonDisclosable: v.NonDisclosable}
		if err := visit(hit, at, state.byteAt); err != nil {
			return err
		}
	}
	return nil
}

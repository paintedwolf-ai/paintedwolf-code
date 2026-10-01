// Package secretspan projects secret matches into value-free text ranges.
package secretspan

import (
	"context"
	"sort"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

type State string

const (
	StateTracked  State = "tracked"
	StateRetired  State = "retired"
	StateDetected State = "detected"
)

// ScanByteCap bounds one document screen.
const ScanByteCap = 512 << 10

// Span is a screened rune range.
type Span struct {
	Start, End int
	State      State
	// RuleID and RuleTitle identify the evidence, never the value.
	RuleID, RuleTitle string
	// Reference is the managed capability. Only StateTracked carries one, and
	// only where the screen's audience may see the capability.
	Reference string
	// Shape contains synthetic character classes and run lengths.
	Shape string
}

type Result struct {
	Spans          []Span
	Truncated      bool
	ScreenedBytes  int
	CatalogVersion string
}

type Screener struct {
	matcher *secretmatch.Matcher
}

func New(matcher *secretmatch.Matcher) *Screener {
	return &Screener{matcher: matcher}
}

func (s *Screener) Ready() bool {
	return s != nil && s.matcher != nil && !s.matcher.Inert()
}

// Screen uses ctx to resolve chat-scoped evidence.
func (s *Screener) Screen(ctx context.Context, text string) *Result {
	if !s.Ready() {
		return nil
	}
	out := &Result{CatalogVersion: s.matcher.CatalogVersion()}
	scanned := text
	if len(scanned) > ScanByteCap {
		scanned = truncateAtRune(scanned, ScanByteCap)
		out.Truncated = true
	}
	out.ScreenedBytes = len(scanned)
	matches := s.matcher.ScreenContext(ctx, scanned)
	out.Spans = make([]Span, 0, len(matches))
	for _, m := range matches {
		if m.End <= m.Start {
			continue
		}
		span := Span{
			Start: m.Start, End: m.End, State: stateOf(m),
			RuleID: m.RuleID, RuleTitle: m.Title, Shape: m.GenericShape,
		}
		if span.State == StateTracked {
			span.Reference = m.Reference
		}
		out.Spans = append(out.Spans, span)
	}
	sort.SliceStable(out.Spans, func(i, j int) bool {
		if out.Spans[i].Start != out.Spans[j].Start {
			return out.Spans[i].Start < out.Spans[j].Start
		}
		return out.Spans[i].End < out.Spans[j].End
	})
	return out
}

// stateOf reads the ledger's standing, never the reference: a live capability
// the audience may not spend still carries no reference.
func stateOf(m secretmatch.Match) State {
	switch {
	case !secretmatch.IsManagedRule(m.RuleID):
		return StateDetected
	case m.Retired:
		return StateRetired
	default:
		return StateTracked
	}
}

// truncateAtRune cuts at or below limit without splitting a rune.
func truncateAtRune(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// ClassificationRevision changes when current fixture corrections change.
func (s *Screener) ClassificationRevision(ctx context.Context) string {
	if s == nil || s.matcher == nil {
		return ""
	}
	return s.matcher.ClassificationRevision(ctx)
}

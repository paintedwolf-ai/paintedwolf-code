package textsearch

import (
	"bytes"
	"context"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
)

// --- fixed strings ---------------------------------------------------------

type literalEngine struct {
	patterns  [][]byte
	line      bool
	fold      bool
	proofWork int64
	limits    Limits
}

func compileFixed(ctx context.Context, patterns []string, spec Spec, limits Limits) (engine, error) {
	if !spec.IgnoreCase && !spec.Word {
		e := literalEngine{line: spec.Line, limits: limits}
		for _, p := range patterns {
			e.patterns = append(e.patterns, []byte(p))
		}
		return e, nil
	}
	quoted := make([]string, len(patterns))
	for i, p := range patterns {
		if !utf8.ValidString(p) {
			return nil, &PatternError{Code: CodeRegexUnsupported, Pattern: p, Detail: "case-folded or word fixed strings must be UTF-8"}
		}
		var b strings.Builder
		for _, r := range p {
			if regexp2.IsMarker(r) {
				return nil, &PatternError{Code: CodeRegexUnsupported, Pattern: p, Detail: "private-use scalar reserved for host assertions"}
			}
			if r < utf8.RuneSelf && strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		quoted[i] = b.String()
	}
	if err := checkPatternBytes(patterns, limits); err != nil {
		return nil, err
	}
	return compileTranslated(ctx, patterns, quotedPOSIX(quoted), spec, limits)
}

func quotedPOSIX(texts []string) []posixPattern {
	out := make([]posixPattern, len(texts))
	for i, text := range texts {
		out[i] = posixPattern{text: text}
	}
	return out
}

// leftmost finds the leftmost occurrence at or after from, preferring the
// longest pattern at that offset.
func (e literalEngine) leftmost(ctx context.Context, record []byte, from int, work *int64) (Span, bool, error) {
	best := Span{-1, -1}
	for _, p := range e.patterns {
		if err := ctx.Err(); err != nil {
			return Span{}, false, err
		}
		// Admit the worst-case byte comparisons before calling the byte matcher.
		if e.fold {
			i, err := e.foldedIndex(ctx, record, p, from, work)
			if err != nil {
				return Span{}, false, err
			}
			if i >= 0 && (best.Start < 0 || i < best.Start || i == best.Start && i+len(p) > best.End) {
				best = Span{i, i + len(p)}
			}
			continue
		}
		cost := int64(len(record)-from+1) * int64(max(1, len(p)))
		if cost > e.limits.WorkSteps-*work {
			return Span{}, false, &BudgetError{Resource: "regex_work_steps", Maximum: e.limits.WorkSteps, Observed: *work + cost}
		}
		*work += cost
		i := bytes.Index(record[from:], p)
		if i < 0 {
			continue
		}
		start := from + i
		if best.Start < 0 || start < best.Start || (start == best.Start && start+len(p) > best.End) {
			best = Span{start, start + len(p)}
		}
	}
	return best, best.Start >= 0, nil
}

func (e literalEngine) first(ctx context.Context, record []byte) (Span, bool, error) {
	if err := ctx.Err(); err != nil {
		return Span{}, false, err
	}
	if e.line {
		work := e.proofWork
		for _, p := range e.patterns {
			if e.fold {
				if len(record) != len(p) {
					continue
				}
				i, err := e.foldedIndex(ctx, record, p, 0, &work)
				if err != nil {
					return Span{}, false, err
				}
				if i == 0 {
					return Span{0, len(record)}, true, nil
				}
				continue
			}
			if len(record) != len(p) {
				continue
			}
			if int64(len(record)) > e.limits.WorkSteps-work {
				return Span{}, false, &BudgetError{Resource: "regex_work_steps", Maximum: e.limits.WorkSteps, Observed: work + int64(len(record))}
			}
			work += int64(len(record))
			if bytes.Equal(record, p) {
				return Span{0, len(record)}, true, nil
			}
		}
		return Span{}, false, nil
	}
	work := e.proofWork
	return e.leftmost(ctx, record, 0, &work)
}

func (e literalEngine) each(ctx context.Context, record []byte, yield func(Span) error) error {
	if e.line {
		span, ok, err := e.first(ctx, record)
		if err != nil || !ok || span.End == span.Start {
			return err
		}
		return yield(span)
	}
	work := e.proofWork
	for from := 0; from <= len(record); {
		if err := ctx.Err(); err != nil {
			return err
		}
		span, ok, err := e.leftmost(ctx, record, from, &work)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if span.End == span.Start {
			from = nextScalar(record, span.Start)
			continue
		}
		if err := yield(span); err != nil {
			return err
		}
		from = span.End
	}
	return nil
}

func (e literalEngine) groups(ctx context.Context, record []byte, yield func([]int) error) error {
	return e.each(ctx, record, func(s Span) error { return yield([]int{s.Start, s.End}) })
}

func (literalEngine) captureGroups() int { return 0 }

func nextScalar(record []byte, at int) int {
	if at >= len(record) {
		return len(record) + 1
	}
	_, size := utf8.DecodeRune(record[at:])
	return at + size
}

func pureLiteralEngine(raw []string, translated []posixPattern, spec Spec, limits Limits) *literalEngine {
	if spec.Word || len(raw) == 0 || len(raw) != len(translated) {
		return nil
	}
	e := literalEngine{line: spec.Line, fold: spec.IgnoreCase, limits: limits, patterns: make([][]byte, len(raw))}
	for i, p := range raw {
		t := translated[i]
		if t.groups != 0 || t.marked || !utf8.ValidString(p) || strings.ContainsRune(p, utf8.RuneError) {
			return nil
		}
		if spec.Dialect != Fixed && (t.text != p || strings.ContainsAny(p, `\.+*?()|[]{}^$`)) {
			return nil
		}
		if e.fold && !asciiString(p) {
			return nil
		}
		for _, r := range p {
			if regexp2.IsMarker(r) {
				return nil
			}
		}
		e.patterns[i] = []byte(p)
	}
	return &e
}

func asciiString(value string) bool {
	for i := range len(value) {
		if value[i] >= 128 {
			return false
		}
	}
	return true
}

func literalRecordProof(ctx context.Context, value []byte, ascii, line bool) (int64, bool) {
	for i, c := range value {
		if ascii && c >= 128 || line && c == '\n' || i&4095 == 0 && ctx.Err() != nil {
			return int64(i + 1), false
		}
	}
	return int64(len(value)), true
}

func asciiFold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// foldedIndex runs only after the pattern and record prove ASCII.
func (e literalEngine) foldedIndex(ctx context.Context, record, pattern []byte, from int, work *int64) (int, error) {
	if len(pattern) == 0 {
		if *work >= e.limits.WorkSteps {
			return -1, &BudgetError{Resource: "regex_work_steps", Maximum: e.limits.WorkSteps, Observed: *work + 1}
		}
		*work += 1
		return from, ctx.Err()
	}
	for at := from; at <= len(record)-len(pattern); at++ {
		candidate, err := e.foldedCandidate(ctx, record, pattern[0], at, len(record)-len(pattern)+1, work)
		if err != nil || candidate < 0 {
			return -1, err
		}
		at = candidate
		matched := true
		for offset, want := range pattern {
			if *work >= e.limits.WorkSteps {
				return -1, &BudgetError{Resource: "regex_work_steps", Maximum: e.limits.WorkSteps, Observed: *work + 1}
			}
			*work += 1
			if asciiFold(record[at+offset]) != asciiFold(want) {
				matched = false
				break
			}
		}
		if matched {
			return at, nil
		}
	}
	return -1, nil
}

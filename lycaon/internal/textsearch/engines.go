package textsearch

import (
	"bytes"
	"context"
	"errors"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	"github.com/lycaon/lycaon/internal/litprefilter"
)

type engine interface {
	first(ctx context.Context, record []byte) (Span, bool, error)
	each(ctx context.Context, record []byte, yield func(Span) error) error
	groups(ctx context.Context, record []byte, yield func([]int) error) error
	captureGroups() int
}

func (l Limits) syntaxBudget() regexp2.SyntaxBudget {
	return regexp2.SyntaxBudget{MaxBytes: l.MemoryBytes, MaxSteps: l.WorkSteps, Reserve: l.Reserve}
}

// budgetError maps the bounded engine's stop onto the catalog limit names.
func budgetError(err error) error {
	var stopped *regexp2.SyntaxBudgetError
	if !errors.As(err, &stopped) {
		return err
	}
	if stopped.Resource == "steps" {
		return &BudgetError{Resource: "regex_work_steps", Maximum: stopped.Maximum, Observed: stopped.Observed}
	}
	return &BudgetError{Resource: "evaluation_memory_bytes", Maximum: stopped.Maximum, Observed: stopped.Observed}
}

// parseError maps a maintained-parser refusal to the invalid-pattern code.
func parseError(pattern string, err error) error {
	var invalid *regexp2.SyntaxParseError
	if errors.As(err, &invalid) {
		return &PatternError{Code: CodeRegexInvalid, Pattern: pattern, Detail: invalid.Error()}
	}
	return budgetError(err)
}

// --- fixed strings ---------------------------------------------------------

type literalEngine struct {
	patterns [][]byte
	line     bool
	limits   Limits
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
		for _, p := range e.patterns {
			if bytes.Equal(record, p) {
				return Span{0, len(record)}, true, nil
			}
		}
		return Span{}, false, nil
	}
	var work int64
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
	var work int64
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

// --- POSIX BRE/ERE -------------------------------------------------------------

func compilePOSIX(ctx context.Context, patterns []string, spec Spec, limits Limits) (engine, error) {
	if err := checkPatternBytes(patterns, limits); err != nil {
		return nil, err
	}
	translated := make([]posixPattern, len(patterns))
	offset := 0
	for i, p := range patterns {
		t, err := translatePOSIX(p, spec.Dialect == Extended, offset, false)
		if err != nil {
			return nil, err
		}
		translated[i] = t
		offset += t.groups
	}
	return compileTranslated(ctx, patterns, translated, spec, limits)
}

// compileTranslated joins translated POSIX alternatives, applies -x and -w,
// and lowers the validated tree into the metered VM.
func compileTranslated(ctx context.Context, raw []string, translated []posixPattern, spec Spec, limits Limits) (engine, error) {
	var text strings.Builder
	marked := spec.Word
	groups := 0
	for i, t := range translated {
		groups += t.groups
		if i > 0 {
			text.WriteByte('|')
		}
		body := "(?:" + t.text + ")"
		if spec.Line {
			body = "^" + body + "$"
		}
		if spec.Word {
			body = string(regexp2.MarkerNotAfterWord) + body + string(regexp2.MarkerNotBeforeWord)
		}
		text.WriteString(body)
		marked = marked || t.marked
	}
	flags := syntax.PerlX | syntax.OneLine | regexp2.ScalarCollation
	if spec.IgnoreCase {
		flags |= syntax.FoldCase
	}
	tree, err := regexp2.ParseSyntaxBounded(ctx, text.String(), flags, limits.syntaxBudget())
	if err != nil {
		return nil, parseError(strings.Join(raw, "\n"), err)
	}
	compile := regexp2.CompileSyntaxBounded
	if marked {
		compile = regexp2.CompileMarkedBounded
	}
	program, err := compile(ctx, tree, limits.syntaxBudget())
	if err != nil {
		return nil, budgetError(err)
	}
	var required litprefilter.Requirement
	if !marked {
		groups = 0
		required = litprefilter.Extract(tree.String(), false)
	}
	return vmEngine{
		program:    program,
		limits:     limits,
		longest:    true,
		groupCount: groups,
		required:   required,
		literal:    pureLiteralEngine(raw, translated, spec, limits),
	}, nil
}

func pureLiteralEngine(raw []string, translated []posixPattern, spec Spec, limits Limits) *literalEngine {
	if spec.IgnoreCase || spec.Word || len(raw) == 0 || len(raw) != len(translated) {
		return nil
	}
	e := literalEngine{line: spec.Line, limits: limits, patterns: make([][]byte, len(raw))}
	for i, p := range raw {
		t := translated[i]
		if t.groups != 0 || t.marked || t.text != p || !utf8.ValidString(p) || strings.ContainsRune(p, utf8.RuneError) || strings.ContainsAny(p, `\.+*?()|[]{}^$`) {
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

// --- RE2 -------------------------------------------------------------------

func compileRE2(ctx context.Context, patterns []string, spec Spec, limits Limits) (engine, error) {
	if err := checkPatternBytes(patterns, limits); err != nil {
		return nil, err
	}
	var text strings.Builder
	if spec.IgnoreCase {
		text.WriteString("(?i)")
	}
	text.WriteString("(?:")
	for i, p := range patterns {
		if _, err := regexp2.ParseSyntaxBounded(ctx, p, syntax.Perl, limits.syntaxBudget()); err != nil {
			return nil, parseError(p, err)
		}
		if i > 0 {
			text.WriteByte('|')
		}
		text.WriteString("(?:" + p + ")")
	}
	text.WriteString(")")
	combined := text.String()
	if spec.Line {
		combined = "^" + combined + "$"
	}
	tree, err := regexp2.ParseSyntaxBounded(ctx, combined, syntax.Perl, limits.syntaxBudget())
	if err != nil {
		return nil, parseError(strings.Join(patterns, "\n"), err)
	}
	compile := regexp2.CompileSyntaxBounded
	if spec.Word {
		compile = regexp2.CompileWordSyntaxBounded
	}
	program, err := compile(ctx, tree, limits.syntaxBudget())
	if err != nil {
		return nil, budgetError(err)
	}
	var required litprefilter.Requirement
	if !spec.Word {
		required = litprefilter.Extract(combined, false)
	}
	var lit *literalEngine
	if len(patterns) == 1 && tree.MaxCap() == 0 {
		lit = pureLiteralEngine(patterns, quotedPOSIX(patterns), spec, limits)
	}
	return vmEngine{program: program, limits: limits, groupCount: tree.MaxCap(), required: required, literal: lit}, nil
}

// --- pcre-common-v1 -----------------------------------------------------------

func compilePerl(ctx context.Context, patterns []string, spec Spec, limits Limits) (engine, error) {
	if err := checkPatternBytes(patterns, limits); err != nil {
		return nil, err
	}
	var text strings.Builder
	offset := 0
	for i, p := range patterns {
		t, err := translatePerl(p, offset)
		if err != nil {
			return nil, err
		}
		offset += t.groups
		if i > 0 {
			text.WriteByte('|')
		}
		body := "(?:" + t.text + ")"
		if spec.Line {
			body = "^" + body + "$"
		}
		if spec.Word {
			body = "(?<!" + pcreWordSet + ")" + body + "(?!" + pcreWordSet + ")"
		}
		text.WriteString(body)
	}
	options := regexp2.None
	if spec.IgnoreCase {
		options |= regexp2.IgnoreCase
	}
	program, err := regexp2.CompilePatternTextBounded(ctx, text.String(), options, limits.syntaxBudget())
	if err != nil {
		var budget *regexp2.SyntaxBudgetError
		if errors.As(err, &budget) {
			return nil, budgetError(err)
		}
		return nil, &PatternError{Code: CodeRegexInvalid, Pattern: strings.Join(patterns, "\n"), Detail: err.Error()}
	}
	return vmEngine{program: program, limits: limits, groupCount: offset}, nil
}

// --- bounded backtracking engine ----------------------------------------------

type vmEngine struct {
	program    *regexp2.BoundedProgram
	limits     Limits
	longest    bool
	groupCount int
	required   litprefilter.Requirement
	literal    *literalEngine
}

func (e vmEngine) canUseLiteral(b []byte) (literalEngine, bool) {
	if e.literal == nil || int64(len(b)) > e.limits.WorkSteps || int64(len(b))*12+8 > e.limits.MemoryBytes {
		return literalEngine{}, false
	}
	lit := *e.literal
	lit.limits = e.limits
	return lit, true
}

// record is one search input as scalars with the byte offset of each.
type record struct {
	runes   []rune
	offsets []int
}

func decodeRecord(b []byte) record {
	r := record{runes: make([]rune, 0, len(b)), offsets: make([]int, 0, len(b)+1)}
	for i := 0; i < len(b); {
		c, size := utf8.DecodeRune(b[i:])
		r.runes = append(r.runes, c)
		r.offsets = append(r.offsets, i)
		i += size
	}
	r.offsets = append(r.offsets, len(b))
	return r
}

func (e vmEngine) search(ctx context.Context, b []byte, all bool, yield func([]int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.required.Empty() && int64(len(b)) <= e.limits.WorkSteps && int64(len(b))*12+8 <= e.limits.MemoryBytes && !e.required.Matches(b) {
		return nil
	}
	session, err := e.program.Session(ctx, e.limits.syntaxBudget())
	if err != nil {
		return budgetError(err)
	}
	defer session.Close()
	if err := session.Charge(int64(len(b))); err != nil {
		return budgetError(err)
	}
	if err := session.ReserveMemory(int64(len(b))*12 + 8); err != nil {
		return budgetError(err)
	}
	rec := decodeRecord(b)
	for pos := 0; pos <= len(rec.runes); {
		groups, err := session.Find(rec.runes, pos, e.longest)
		if err != nil {
			return budgetError(err)
		}
		if groups == nil {
			return nil
		}
		start, end := groups[0], groups[1]
		if !all || end > start {
			for i, v := range groups {
				if v >= 0 {
					groups[i] = rec.offsets[v]
				}
			}
			if err := yield(groups); err != nil {
				return err
			}
			if !all {
				return nil
			}
		}
		if end > start {
			pos = end
		} else {
			pos = start + 1
		}
	}
	return nil
}

func (e vmEngine) first(ctx context.Context, b []byte) (Span, bool, error) {
	if lit, ok := e.canUseLiteral(b); ok {
		return lit.first(ctx, b)
	}
	var span Span
	found := false
	err := e.search(ctx, b, false, func(g []int) error {
		span, found = Span{g[0], g[1]}, true
		return nil
	})
	return span, found, err
}

func (e vmEngine) each(ctx context.Context, b []byte, yield func(Span) error) error {
	if lit, ok := e.canUseLiteral(b); ok {
		return lit.each(ctx, b, yield)
	}
	return e.search(ctx, b, true, func(g []int) error { return yield(Span{g[0], g[1]}) })
}

func (e vmEngine) groups(ctx context.Context, b []byte, yield func([]int) error) error {
	if lit, ok := e.canUseLiteral(b); ok {
		return lit.groups(ctx, b, yield)
	}
	return e.search(ctx, b, true, func(g []int) error {
		if e.groupCount == 0 {
			g = g[:2]
		}
		return yield(g)
	})
}

func (e vmEngine) captureGroups() int { return e.groupCount }

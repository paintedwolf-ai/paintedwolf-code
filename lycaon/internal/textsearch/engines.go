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
	var literal *literalEngine
	if len(patterns) == 1 && offset == 0 {
		literal = pureLiteralEngine(patterns, quotedPOSIX(patterns), spec, limits)
	}
	return vmEngine{program: program, limits: limits, groupCount: offset, literal: literal}, nil
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

func (e vmEngine) canUseLiteral(ctx context.Context, b []byte) (literalEngine, bool) {
	if e.literal == nil || int64(len(b)) > e.limits.WorkSteps || int64(len(b))*12+8 > e.limits.MemoryBytes {
		return literalEngine{}, false
	}
	lit := *e.literal
	lit.limits = e.limits
	if lit.line && bytes.IndexByte(b, '\n') >= 0 {
		return literalEngine{}, false
	}
	if lit.fold {
		if !asciiRecord(ctx, b) {
			return literalEngine{}, false
		}
		lit.proofWork = int64(len(b))
	}
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
	if lit, ok := e.canUseLiteral(ctx, b); ok {
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
	if lit, ok := e.canUseLiteral(ctx, b); ok {
		return lit.each(ctx, b, yield)
	}
	return e.search(ctx, b, true, func(g []int) error { return yield(Span{g[0], g[1]}) })
}

func (e vmEngine) groups(ctx context.Context, b []byte, yield func([]int) error) error {
	if lit, ok := e.canUseLiteral(ctx, b); ok {
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

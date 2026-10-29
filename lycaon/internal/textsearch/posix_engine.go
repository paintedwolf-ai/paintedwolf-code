package textsearch

import (
	"context"
	"regexp/syntax"

	"github.com/dlclark/regexp2/v2"
)

// PosixRegex is one compiled BRE or ERE with POSIX leftmost-longest matching
// and POSIX subexpression offsets.
type PosixRegex interface {
	// Find returns the leftmost-longest match starting at or after the byte
	// offset from as byte offsets [start, end, group1 start, group1 end, ...]
	// (-1 when a group is unset), or nil. Empty matches are reported. Anchors,
	// references and word assertions see the whole subject.
	Find(ctx context.Context, subject []byte, from int) ([]int, error)
	// Groups is the number of capture groups a replacement may name.
	Groups() int
}

// POSIXEngine compiles BRE and ERE text for a pattern space (sed): the same
// GNU dialects grep uses, plus the \n and \t escapes, with "." matching any
// scalar including LF and ^ and $ anchoring the whole subject. Every pattern
// runs on the bounded engine, so subexpression offsets follow POSIX: the
// longest overall match, then for each group in order the earliest start and
// the longest extent. Each Find is metered as one record.
type POSIXEngine struct{ Limits Limits }

// NewPOSIXEngine reads the catalog regex ceilings.
func NewPOSIXEngine() (POSIXEngine, error) {
	limits, err := CatalogLimits()
	return POSIXEngine{Limits: limits}, err
}

func (e POSIXEngine) Compile(ctx context.Context, pattern string, extended bool) (PosixRegex, error) {
	return e.compile(ctx, pattern, extended, false)
}

// CompileFolded compiles with Unicode simple case folding (sed's I flag).
func (e POSIXEngine) CompileFolded(ctx context.Context, pattern string, extended bool) (PosixRegex, error) {
	return e.compile(ctx, pattern, extended, true)
}

func (e POSIXEngine) compile(ctx context.Context, pattern string, extended, fold bool) (PosixRegex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkPatternBytes([]string{pattern}, e.Limits); err != nil {
		return nil, err
	}
	t, err := translatePOSIX(pattern, extended, 0, true)
	if err != nil {
		return nil, err
	}
	flags := syntax.PerlX | syntax.OneLine | syntax.DotNL | syntax.ClassNL | regexp2.ScalarCollation
	if fold {
		flags |= syntax.FoldCase
	}
	tree, err := regexp2.ParseSyntaxBounded(ctx, t.text, flags, e.Limits.syntaxBudget())
	if err != nil {
		return nil, parseError(pattern, err)
	}
	program, err := regexp2.CompileMarkedBounded(ctx, tree, e.Limits.syntaxBudget())
	if err != nil {
		return nil, budgetError(err)
	}
	return posixRegex{vm: vmEngine{program: program, limits: e.Limits, longest: true, groupCount: t.groups}}, nil
}

type posixRegex struct{ vm vmEngine }

func (r posixRegex) Groups() int { return r.vm.groupCount }

func (r posixRegex) Find(ctx context.Context, subject []byte, from int) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if from < 0 || from > len(subject) {
		return nil, nil
	}
	if int64(len(subject)) > r.vm.limits.InputBytes {
		return nil, &BudgetError{Resource: "regex_input_bytes", Maximum: r.vm.limits.InputBytes, Observed: int64(len(subject))}
	}
	session, err := r.vm.program.Session(ctx, r.vm.limits.syntaxBudget())
	if err != nil {
		return nil, budgetError(err)
	}
	defer session.Close()
	if err := session.Charge(int64(len(subject))); err != nil {
		return nil, budgetError(err)
	}
	rec := decodeRecord(subject)
	start := 0
	for start < len(rec.runes) && rec.offsets[start] < from {
		start++
	}
	groups, err := session.Find(rec.runes, start, true)
	if err != nil || groups == nil {
		return nil, budgetError(err)
	}
	for i, v := range groups {
		if v >= 0 {
			groups[i] = rec.offsets[v]
		}
	}
	return groups, nil
}

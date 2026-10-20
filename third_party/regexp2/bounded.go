package regexp2

import (
	"context"
	"regexp/syntax"

	"github.com/dlclark/regexp2/v2/internal/budget"
	enginesyntax "github.com/dlclark/regexp2/v2/syntax"
	posixsyntax "github.com/dlclark/regexp2/v2/syntax/posix"
)

// SyntaxBudget bounds only MatchSyntaxBounded, including its typed compilation.
// Reserve joins the host aggregate working-memory admission before allocation.
// A nil Reserve selects pure local accounting and provides no aggregate claim.
type SyntaxBudget = budget.Limits
type SyntaxBudgetError = budget.Error
type SyntaxParseError = posixsyntax.Error

// MatchSyntaxBounded executes only a host-validated regular tree. It exposes no
// text parser, captures, generated engine, pool retention, or PCRE qualification.
// Cancellation/deadline/work checks run before each VM iteration and scalar loop;
// allocation payload accounting retains old/new growth overlap until return.
func MatchSyntaxBounded(ctx context.Context, source *syntax.Regexp, input string, limits SyntaxBudget) (matched bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if stopped, ok := recovered.(budget.Abort); ok {
				matched = false
				err = stopped.Err
			} else {
				panic(recovered)
			}
		}
	}()
	m := budget.New(ctx, limits)
	defer m.Close()
	return matchSyntaxWithMeter(source, input, m)
}

func matchSyntaxWithMeter(source *syntax.Regexp, input string, m *budget.Meter) (bool, error) {
	tree := enginesyntax.FromValidatedSyntax(source, m)
	code, err := enginesyntax.WriteMetered(tree, m)
	if err != nil {
		return false, err
	}
	m.Alloc(1024)
	re := &Regexp{code: code, capsize: 1, MatchTimeout: DefaultMatchTimeout, optimizations: OptimizationOptions{MaxBacktrackingStackSize: -1}}
	runner := &Runner{re: re, code: code, budget: m}
	count := 0
	for range input {
		m.Step(1)
		count++
	}
	m.Alloc(int64(count) * 4)
	text := make([]rune, 0, count)
	for _, ch := range input {
		m.Step(1)
		text = append(text, ch)
	}
	result, err := runner.scan(text, nil, 0, true, DefaultMatchTimeout)
	if err != nil {
		return false, err
	}
	m.Check()
	return result != nil && result.matchcount[0] > 0, nil
}

// MatchPatternBounded meters the maintained selected Go syntax parser as well as
// typed compilation and matching. Transform restores host-normalized counts;
// it is host code over the detached tree and accepts no executable program.
func MatchPatternBounded(ctx context.Context, source, input string, flags syntax.Flags, transform func(*syntax.Regexp) error, limits SyntaxBudget) (matched bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if stopped, ok := recovered.(budget.Abort); ok {
				matched = false
				err = stopped.Err
			} else {
				panic(recovered)
			}
		}
	}()
	m := budget.New(ctx, limits)
	defer m.Close()
	ast, err := posixsyntax.ParseMetered(source, flags, m)
	if err != nil {
		return false, err
	}
	if transform != nil {
		m.Step(1)
		if err := transform(ast); err != nil {
			return false, err
		}
	}
	return matchSyntaxWithMeter(ast, input, m)
}

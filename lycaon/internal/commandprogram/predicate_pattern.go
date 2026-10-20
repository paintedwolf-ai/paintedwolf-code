package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/pattern"
)

// PredicateMemory joins the invoking program's aggregate allocation admission.
type PredicateMemory interface {
	Reserve(context.Context, int64) (func(), error)
}

// NativePredicatePatterns preserves the selected shell glob and POSIX grammar.
// The bounded existing VM exposes no PCRE grammar or captures.
type NativePredicatePatterns struct {
	Memory          PredicateMemory
	LocalBoundsOnly bool
}

var predicatePatternLimits = sync.OnceValue(func() map[string]int64 {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		panic(err)
	}
	limits := map[string]int64{}
	for _, limit := range catalog.Language.Limits {
		limits[limit.ID] = limit.Maximum
	}
	return limits
})

func (p NativePredicatePatterns) MatchPredicate(ctx context.Context, kind, input string, value PredicateValue) (matched bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	limits := predicatePatternLimits()
	callerContext := ctx
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits["regex_cpu_ms"])*time.Millisecond)
	defer cancel()
	defer func() {
		if err != nil && callerContext.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			err = &PredicateLimitError{"regex_cpu_ms", limits["regex_cpu_ms"], limits["regex_cpu_ms"] + 1}
		}
	}()
	var raw int64
	for i, fragment := range value {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if int64(i+1) > limits["regex_work_steps"] {
			return false, &PredicateLimitError{"regex_work_steps", limits["regex_work_steps"], int64(i + 1)}
		}
		raw += int64(len(fragment.Value))
		if raw > limits["regex_pattern_bytes"] {
			return false, &PredicateLimitError{"regex_pattern_bytes", limits["regex_pattern_bytes"], raw}
		}
	}
	if raw > limits["regex_pattern_bytes"] {
		return false, &PredicateLimitError{"regex_pattern_bytes", limits["regex_pattern_bytes"], raw}
	}
	if int64(len(input)) > limits["regex_input_bytes"] {
		return false, &PredicateLimitError{"regex_input_bytes", limits["regex_input_bytes"], int64(len(input))}
	}
	if p.Memory == nil && !p.LocalBoundsOnly {
		return false, fmt.Errorf("native predicate patterns require aggregate memory admission")
	}
	// Lexical normalization is bounded by the submitted512-byte pattern and
	// reserves old/new string/tag capacities before building intermediary data.
	// The selected parser, writer and VM share the remaining dynamic admission.
	lexicalBytes := 1024 + (raw+1)*(raw+1)*8
	if p.Memory != nil {
		release, err := p.Memory.Reserve(ctx, lexicalBytes)
		if err != nil {
			return false, err
		}
		defer release()
	}
	var source strings.Builder
	for _, fragment := range value {
		text := fragment.Value
		if fragment.Quoted {
			if kind == "glob" {
				text = pattern.QuoteMeta(text, 0)
			} else {
				text = regexp.QuoteMeta(text)
			}
		}
		source.WriteString(text)
	}
	var expression string
	var flags syntax.Flags
	var transform func(*syntax.Regexp) error
	switch kind {
	case "glob":
		translated, err := pattern.Regexp(source.String(), pattern.EntireString)
		if err != nil {
			return false, errPredicateUsage
		}
		expression = translated
		flags = syntax.Perl
	case "ere":
		skeleton, tags, err := normalizePredicateRepeats(ctx, source.String())
		if err != nil {
			return false, err
		}
		expression = skeleton
		flags = syntax.POSIX | syntax.DotNL | syntax.ClassNL | syntax.OneLine
		transform = func(ast *syntax.Regexp) error { return restorePredicateRepeats(ctx, ast, tags) }
	default:
		return false, errPredicateUsage
	}
	reserve := func(context.Context, int64) (func(), error) { return func() {}, nil }
	if p.Memory != nil {
		reserve = p.Memory.Reserve
	}
	matched, err = regexp2.MatchPatternBounded(ctx, expression, input, flags, transform, regexp2.SyntaxBudget{MaxBytes: limits["evaluation_memory_bytes"] - lexicalBytes, MaxSteps: limits["regex_work_steps"], Timeout: time.Duration(limits["regex_cpu_ms"]) * time.Millisecond, Reserve: reserve})
	var stopped *regexp2.SyntaxBudgetError
	if errors.As(err, &stopped) {
		resource := "evaluation_memory_bytes"
		if stopped.Resource == "steps" {
			resource = "regex_work_steps"
		}
		if stopped.Resource == "time" {
			resource = "regex_cpu_ms"
		}
		maximum, observed := stopped.Maximum, stopped.Observed
		if stopped.Resource == "time" {
			maximum /= int64(time.Millisecond)
			observed = (observed + int64(time.Millisecond) - 1) / int64(time.Millisecond)
		}
		return false, &PredicateLimitError{resource, maximum, observed}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		if errors.Is(err, errPredicateUsage) {
			return false, err
		}
		// Maintained syntax parser errors are ordinary utility usage failures.
		var invalid *regexp2.SyntaxParseError
		if errors.As(err, &invalid) {
			return false, errPredicateUsage
		}
	}
	return matched, err
}

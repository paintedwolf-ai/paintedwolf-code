package commandprogram

import (
	"context"
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoundedPredicateEngineResourceAndCancellationControls(t *testing.T) {
	baseline := regexp2.SyntaxBudget{MaxBytes: 32 << 20, MaxSteps: 1_000_000, Timeout: time.Hour}
	for _, resource := range []string{"bytes", "steps", "time"} {
		t.Run(resource, func(t *testing.T) {
			limits := baseline
			switch resource {
			case "bytes":
				limits.MaxBytes = 1
			case "steps":
				limits.MaxSteps = 1
			case "time":
				limits.Timeout = time.Nanosecond
			}
			_, err := regexp2.MatchPatternBounded(t.Context(), "(a+)+$", strings.Repeat("a", 100)+"!", syntax.POSIX, nil, limits)
			var stopped *regexp2.SyntaxBudgetError
			if !errors.As(err, &stopped) || stopped.Resource != resource {
				t.Fatalf("missing exact stopping fact: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reserved, released := int64(0), int64(0)
	limits := baseline
	limits.Reserve = func(_ context.Context, n int64) (func(), error) {
		reserved += n
		cancel()
		return func() { released += n }, nil
	}
	_, err := regexp2.MatchPatternBounded(ctx, "a[[:alpha:]]+", "abc", syntax.POSIX, nil, limits)
	if !errors.Is(err, context.Canceled) || reserved == 0 || released != reserved {
		t.Fatalf("cancel/reservation settlement err%v reserved%d released%d", err, reserved, released)
	}
}
func TestBoundedPredicateEREPreservesMaintainedGrammarResults(t *testing.T) {
	patterns := []string{"", "a|ab", "(a|b)+", "a{2,4}", "(ab?){0,3}", "[[:alpha:][:digit:]_]", "[^a-c]", "^a.b$", "[é-ê]+", `\(x\)`, "(a{1}|a{2}){2}"}
	inputs := []string{"", "a", "ab", "abc", "aaaa", "b", "0", "x\ny", "a\nb", "éê", "(x)"}
	for _, source := range patterns {
		// Reference probes preserve REG_NEWLINE-off semantics. They qualify
		// adapter equivalence, independently of native/installed qualification.
		ast, err := syntax.Parse(source, syntax.POSIX|syntax.ClassNL|syntax.DotNL|syntax.OneLine)
		testutil.FailErr(t, "reference ERE grammar", err)
		expected, err := regexp.Compile(ast.String())
		testutil.FailErr(t, "reference regular matcher", err)
		for _, input := range inputs {
			got, err := (NativePredicatePatterns{LocalBoundsOnly: true}).MatchPredicate(t.Context(), "ere", input, PredicateValue{{Value: source}})
			testutil.FailErr(t, "bounded native ERE", err)
			if got != expected.MatchString(input) {
				t.Fatalf("pattern%q input%q native%v reference%v", source, input, got, expected.MatchString(input))
			}
		}
	}
}

type predicateMemoryFunc func(context.Context, int64) (func(), error)

func (f predicateMemoryFunc) Reserve(ctx context.Context, n int64) (func(), error) { return f(ctx, n) }
func TestPredicateWorkingMemoryRefusalCannotBecomeFalse(t *testing.T) {
	cause := errors.New("aggregate memory exhausted")
	memory := predicateMemoryFunc(func(context.Context, int64) (func(), error) { return nil, cause })
	_, err := (PredicateRuntime{Memory: memory}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"x"})
	if !errors.Is(err, cause) {
		t.Fatalf("argv allocation refusal lost %v", err)
	}
	_, err = (NativePredicatePatterns{Memory: memory}).MatchPredicate(t.Context(), "ere", "a", PredicateValue{{Value: "a"}})
	if !errors.Is(err, cause) {
		t.Fatalf("pattern allocation refusal lost %v", err)
	}
	if _, err := (PredicateRuntime{}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"x"}); err == nil {
		t.Fatal("missing aggregate port silently bypassed")
	}
}

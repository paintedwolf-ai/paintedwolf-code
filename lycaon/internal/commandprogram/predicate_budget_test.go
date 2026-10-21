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
	"github.com/lycaon/lycaon/internal/toolcontract"
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

func predicateMemory(t *testing.T, working int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: working, State: 1})
	testutil.FailErr(t, "program memory", err)
	return memory
}

func TestPredicateWorkingMemoryRefusalCannotBecomeFalse(t *testing.T) {
	var limit *toolcontract.ProgramMemoryLimitError
	exhausted := predicateMemory(t, 1)
	_, err := (PredicateRuntime{Memory: exhausted}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"x"})
	if !errors.As(err, &limit) || limit.Account != "working" {
		t.Fatalf("argv allocation refusal lost %v", err)
	}
	_, err = (NativePredicatePatterns{Memory: exhausted}).MatchPredicate(t.Context(), "ere", "a", PredicateValue{{Value: "a"}})
	if !errors.As(err, &limit) {
		t.Fatalf("pattern allocation refusal lost %v", err)
	}
	if _, err := (PredicateRuntime{}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"x"}); err == nil {
		t.Fatal("missing aggregate port silently bypassed")
	}
}

func TestPredicateRefusalReleasesEarlierLeases(t *testing.T) {
	for _, working := range []int64{512, 560, 600} {
		memory := predicateMemory(t, working)
		_, err := (PredicateRuntime{Memory: memory}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"a", "=", "a"})
		var limit *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &limit) || memory.InUse().Working != 0 || memory.Peak().Working == 0 {
			t.Fatalf("working%d err%v in use%+v peak%+v", working, err, memory.InUse(), memory.Peak())
		}
	}
	memory := predicateMemory(t, 1<<20)
	if got, err := (PredicateRuntime{Memory: memory}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"a", "=", "a"}); err != nil || got.Status != 0 || memory.InUse().Working != 0 {
		t.Fatalf("admitted comparison %+v %v in use %+v", got, err, memory.InUse())
	}
	missing := func(context.Context, int64) (func(), error) { return nil, nil }
	limits := regexp2.SyntaxBudget{MaxBytes: 32 << 20, MaxSteps: 1_000_000, Timeout: time.Second, Reserve: missing}
	if _, err := regexp2.MatchPatternBounded(t.Context(), "a", "a", syntax.POSIX, nil, limits); err == nil {
		t.Fatal("selected engine admitted missing lease")
	}
}

func TestBoundedPredicateLiteralParserChargesBeforeGrowth(t *testing.T) {
	limits := regexp2.SyntaxBudget{MaxBytes: 1024, MaxSteps: 1_000_000, Timeout: time.Second}
	_, err := regexp2.MatchPatternBounded(t.Context(), strings.Repeat("x", 4096), "x", syntax.Literal, nil, limits)
	var stopped *regexp2.SyntaxBudgetError
	if !errors.As(err, &stopped) || stopped.Resource != "bytes" {
		t.Fatalf("literal conversion escaped memory budget: %v", err)
	}
	limits.MaxBytes = 32 << 20
	got, err := regexp2.MatchPatternBounded(t.Context(), "a.b", "prefix a.b", syntax.Literal, nil, limits)
	if err != nil || !got {
		t.Fatalf("literal semantics got%v err%v", got, err)
	}
}

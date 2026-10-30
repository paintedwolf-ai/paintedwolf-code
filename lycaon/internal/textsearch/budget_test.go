package textsearch

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func budgetResource(t *testing.T, err error) string {
	t.Helper()
	var budget *BudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("want budget error, got %v", err)
	}
	return budget.Resource
}

// TestRegexBudgets exercises work exhaustion across every regex dialect,
// plus pattern and record bounds.
func TestRegexBudgets(t *testing.T) {
	limits := catalogLimits(t)
	ctx := t.Context()
	linear, err := Compile(ctx, Spec{Patterns: []string{`b`}, Dialect: Extended}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	at := []byte(strings.Repeat("a", int(limits.WorkSteps)-1))
	if _, ok, err := linear.Select(ctx, at); err != nil || ok {
		t.Fatalf("record at the step ceiling: %v %v", ok, err)
	}
	at = append(at, 'a')
	if _, _, err := linear.Select(ctx, at); budgetResource(t, err) != "regex_work_steps" {
		t.Fatalf("record over the step ceiling: %v", err)
	}

	catastrophic := []byte(strings.Repeat("a", 40) + "bc" + strings.Repeat("a", 40))
	for _, spec := range []Spec{
		{Patterns: []string{`^(a*)*b$`}, Dialect: Perl},
		{Patterns: []string{`^\(a*\)*\1b$`}, Dialect: Basic},
		{Patterns: []string{`^(a|aa)*c$`}, Dialect: Extended},
		{Patterns: []string{`^(a|aa)*c$`}, Dialect: RE2},
	} {
		m, err := Compile(ctx, spec, limits)
		if err != nil {
			t.Fatalf("%v: %v", spec.Patterns, err)
		}
		if _, _, err := m.Select(ctx, catastrophic); budgetResource(t, err) != "regex_work_steps" {
			t.Fatalf("%v select: %v", spec.Patterns, err)
		}
	}

	long := strings.Repeat("a", int(limits.PatternBytes)+1)
	for _, dialect := range []Dialect{Basic, Extended, Perl, RE2} {
		if _, err := Compile(ctx, Spec{Patterns: []string{long}, Dialect: dialect}, limits); budgetResource(t, err) != "regex_pattern_bytes" {
			t.Fatalf("%s pattern bytes: %v", dialect, err)
		}
	}
	if _, err := Compile(ctx, Spec{Patterns: []string{long}, Dialect: Fixed}, limits); err != nil {
		t.Fatalf("fixed strings are not regex patterns: %v", err)
	}

	small := limits
	small.InputBytes = 4
	for _, dialect := range []Dialect{Fixed, Basic, Perl} {
		m, err := Compile(ctx, Spec{Patterns: []string{"a"}, Dialect: dialect}, small)
		testutil.FailErr(t, "compile text-search pattern", err)
		if _, _, err := m.Select(ctx, []byte("aaaaa")); budgetResource(t, err) != "regex_input_bytes" {
			t.Fatalf("%s record bytes: %v", dialect, err)
		}
	}

	m, err := Compile(ctx, Spec{Patterns: []string{`(a|b)*c`}, Dialect: Perl}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	vm := m.engine.(vmEngine)
	vm.limits.CPU = time.Nanosecond
	m.engine = vm
	if _, _, err := m.Select(ctx, []byte(strings.Repeat("ab", 2000))); budgetResource(t, err) != "regex_cpu_ms" {
		t.Fatalf("cpu ceiling: %v", err)
	}
}

// TestBoundedSessionStepBoundary is the catalog's injected work-step pair:
// exactly regex_work_steps is admitted and one more step stops the record.
func TestBoundedSessionStepBoundary(t *testing.T) {
	limits := catalogLimits(t)
	m, err := Compile(t.Context(), Spec{Patterns: []string{`(?=a)`}, Dialect: Perl}, limits)
	testutil.FailErr(t, "compile text-search pattern", err)
	program := m.engine.(vmEngine).program
	session, err := program.Session(t.Context(), limits.syntaxBudget())
	testutil.FailErr(t, "open bounded regex session", err)
	defer session.Close()
	if err := session.Charge(limits.WorkSteps); err != nil {
		t.Fatalf("at ceiling: %v", err)
	}
	if err := session.Charge(1); budgetResource(t, budgetError(err)) != "regex_work_steps" {
		t.Fatalf("over ceiling: %v", err)
	}
}

func TestCompileHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Compile(ctx, Spec{Patterns: []string{"a"}, Dialect: Basic}, catalogLimits(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compile: %v", err)
	}
}

func TestErrorsNameTheirCodes(t *testing.T) {
	if got := (&PatternError{Code: CodeRegexInvalid, Pattern: "(", Offset: 1, Detail: "x"}).Error(); !strings.Contains(got, CodeRegexInvalid) {
		t.Fatal(got)
	}
	if got := (&BudgetError{Resource: "regex_work_steps", Maximum: 1, Observed: 2}).Error(); !strings.Contains(got, CodeMatchBudget) || !strings.Contains(got, "regex_work_steps") {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m, err := Compile(t.Context(), Spec{Dialect: Basic}, catalogLimits(t))
	testutil.FailErr(t, "compile text-search pattern", err)
	if err := m.Each(ctx, []byte("a"), func(Span) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty each: %v", err)
	}
	if err := m.Groups(ctx, []byte("a"), func([]int) error { return nil }); !errors.Is(err, context.Canceled) || m.CaptureGroups() != 0 {
		t.Fatalf("empty groups: %v", err)
	}
	if _, _, err := (RegexpSelector{Re: regexp.MustCompile("a")}).Select(ctx, []byte("a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("regexp selector: %v", err)
	}
	if span, ok, err := (RegexpSelector{Re: regexp.MustCompile("b")}).Select(t.Context(), []byte("ab")); err != nil || !ok || span != (Span{1, 2}) {
		t.Fatalf("regexp selector span: %v %v %v", span, ok, err)
	}
}

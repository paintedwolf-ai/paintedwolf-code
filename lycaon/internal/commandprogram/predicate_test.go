package commandprogram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type predicateFileFunc func(context.Context, toolcontract.OperationInvocation, string, string) (bool, error)

func (f predicateFileFunc) TestPath(c context.Context, i toolcontract.OperationInvocation, op, path string) (bool, error) {
	return f(c, i, op, path)
}

type predicateExpansionFunc func(context.Context, commandsurface.DeferredWord) (PredicateValue, error)

func (f predicateExpansionFunc) ExpandPredicate(c context.Context, w commandsurface.DeferredWord) (PredicateValue, error) {
	return f(c, w)
}
func TestNativeBooleanOperandsAreData(t *testing.T) {
	for _, name := range []string{"true", "false", ":"} {
		got, err := BooleanBuiltin(t.Context(), name, []string{"--unknown", "$(never executed)"})
		testutil.FailErr(t, "boolean builtin", err)
		want := 0
		if name == "false" {
			want = 1
		}
		if got.Status != want || !got.Complete || got.PublicationEligible != (want == 0) {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	got, err := BooleanBuiltin(t.Context(), "true", []string{"a\x00b"})
	testutil.FailErr(t, "reject NUL", err)
	if got.Status != 2 {
		t.Fatalf("NUL accepted: %+v", got)
	}
}
func TestArgvPredicates(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		status int
	}{
		{"test", nil, 1}, {"test", []string{""}, 1}, {"test", []string{"!"}, 0}, {"test", []string{"-n"}, 0},
		{"test", []string{"-n", ""}, 1}, {"test", []string{"-z", ""}, 0}, {"test", []string{"-n", "=", "-n"}, 0},
		{"test", []string{"x", "!=", "x"}, 1}, {"test", []string{"x", "=", "y"}, 1},
		{"test", []string{"0000009", "-eq", "+9"}, 0}, {"test", []string{"-0", "-eq", "0"}, 0},
		{"test", []string{strings.Repeat("9", 100), "-gt", "9223372036854775807"}, 0},
		{"test", []string{"-100", "-lt", "-2"}, 0}, {"test", []string{"1+1", "-eq", "2"}, 2},
		{"test", []string{"!", "x", "-o", "y"}, 0}, {"test", []string{"x", "-o", "", "-a", ""}, 0},
		{"test", []string{"(", "x", "-o", "", ")", "-a", ""}, 1},
		{"test", []string{"x", "unexpected"}, 2}, {"test", []string{"x", "-a"}, 2},
		{"[", []string{"x", "]"}, 0}, {"[", []string{"x"}, 2}, {"[", []string{"]"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name+" "+strings.Join(c.args, " "), func(t *testing.T) {
			got, err := (PredicateRuntime{LocalBoundsOnly: true}).TestBuiltin(t.Context(), fixtureInvocation(), c.name, c.args)
			testutil.FailErr(t, "evaluate predicate", err)
			if got.Status != c.status || got.Complete != (c.status < 2) {
				t.Fatalf("got %+v want status %d", got, c.status)
			}
		})
	}
}
func TestPredicateValidationAndShortCircuitProtectAuthority(t *testing.T) {
	cause := errors.New("authority refused")
	probes := 0
	r := PredicateRuntime{LocalBoundsOnly: true, Files: predicateFileFunc(func(context.Context, toolcontract.OperationInvocation, string, string) (bool, error) {
		probes++
		return false, cause
	})}
	got, err := r.TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"-e", "file", "unexpected"})
	testutil.FailErr(t, "reject whole expression", err)
	if got.Status != 2 || probes != 0 {
		t.Fatalf("malformed probe: %+v probes%d", got, probes)
	}
	got, err = r.TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"x", "-o", "-e", "file"})
	testutil.FailErr(t, "short circuit", err)
	if got.Status != 0 || probes != 0 {
		t.Fatalf("short circuit %+v probes%d", got, probes)
	}
	_, err = r.TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{"!", "-e", "file"})
	if !errors.Is(err, cause) || probes != 1 {
		t.Fatalf("refusal booleanized: %v probes%d", err, probes)
	}
}
func predicateWord(value string) commandsurface.TestWord {
	return commandsurface.TestWord{Word: commandsurface.DeferredWord{Parts: []commandsurface.WordNode{commandsurface.LiteralPart{Value: value}}}}
}
func TestTypedPredicateDoesNotExpandUnreachedSubtree(t *testing.T) {
	calls := 0
	r := PredicateRuntime{LocalBoundsOnly: true, Expansion: predicateExpansionFunc(func(_ context.Context, w commandsurface.DeferredWord) (PredicateValue, error) {
		calls++
		return PredicateValue{{Value: w.Parts[0].(commandsurface.LiteralPart).Value}}, nil
	})}
	expression := commandsurface.TestExpression{Expression: commandsurface.BinaryPredicate{Operator: "||", Left: predicateWord("true"), Right: commandsurface.UnaryPredicate{Operator: "-e", Operand: predicateWord("unreached")}}}
	got, err := r.TestExpression(t.Context(), fixtureInvocation(), expression)
	testutil.FailErr(t, "typed short circuit", err)
	if got.Status != 0 || calls != 1 {
		t.Fatalf("result %+v expansions%d", got, calls)
	}
	expression.Expression = commandsurface.BinaryPredicate{Operator: "||", Left: predicateWord("true"), Right: commandsurface.UnaryPredicate{Operator: "-unsupported", Operand: predicateWord("unreached")}}
	got, err = r.TestExpression(t.Context(), fixtureInvocation(), expression)
	testutil.FailErr(t, "validate unreached syntax", err)
	if got.Status != 2 || calls != 1 {
		t.Fatalf("invalid syntax expanded %+v calls%d", got, calls)
	}
}
func TestPredicateCancellationAndBoundsStop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (PredicateRuntime{LocalBoundsOnly: true}).TestBuiltin(ctx, fixtureInvocation(), "test", []string{"x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost %v", err)
	}
	_, err = (PredicateRuntime{LocalBoundsOnly: true}).TestBuiltin(t.Context(), fixtureInvocation(), "test", []string{strings.Repeat("x", int(predicateBounds().argument)+1)})
	var limit *PredicateLimitError
	if !errors.As(err, &limit) || limit.Resource != "argument_bytes" {
		t.Fatalf("missing scalar limit %v", err)
	}
}

package commandprogram

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTypedPredicateOperatorsAndMixedQuotation(t *testing.T) {
	cases := []struct {
		op, left string
		right    PredicateValue
		status   int
	}{
		{"==", "a*bZZ", PredicateValue{{Value: "a*", Quoted: true}, {Value: "b*"}}, 0},
		{"!=", "abc", PredicateValue{{Value: "a*", Quoted: true}}, 0},
		{"=", "abc", PredicateValue{{Value: "[a-c]*"}}, 0},
		{"=~", "x a.b y", PredicateValue{{Value: "a.b", Quoted: true}}, 0},
		{"=~", "axb", PredicateValue{{Value: "a.b", Quoted: true}}, 1},
		{"-eq", "+0009", PredicateValue{{Value: "9"}}, 0},
		{"-ne", "9", PredicateValue{{Value: "8"}}, 0},
		{"-lt", "-12", PredicateValue{{Value: "-2"}}, 0},
		{"-le", "-12", PredicateValue{{Value: "-12"}}, 0},
		{"-gt", "12", PredicateValue{{Value: "2"}}, 0},
		{"-ge", "12", PredicateValue{{Value: "12"}}, 0},
		{"-eq", "1+1", PredicateValue{{Value: "2"}}, 2},
	}
	for _, c := range cases {
		t.Run(c.op+c.left, func(t *testing.T) {
			r := PredicateRuntime{LocalBoundsOnly: true, Patterns: NativePredicatePatterns{LocalBoundsOnly: true}, Expansion: predicateExpansionFunc(func(_ context.Context, w commandsurface.DeferredWord) (PredicateValue, error) {
				if w.ID == "right" {
					return c.right, nil
				}
				return PredicateValue{{Value: c.left}}, nil
			})}
			left := predicateWord("")
			left.Word.ID = "left"
			right := predicateWord("")
			right.Word.ID = "right"
			expression := commandsurface.TestExpression{Expression: commandsurface.BinaryPredicate{Operator: c.op, Left: left, Right: right}}
			got, err := r.TestExpression(t.Context(), fixtureInvocation(), expression)
			testutil.FailErr(t, "typed predicate operation", err)
			if got.Status != c.status {
				t.Fatalf("result %+v want%d", got, c.status)
			}
		})
	}
}
func TestTypedPredicateRetainsHostStopFromDeferredExpansion(t *testing.T) {
	cause := errors.New("substitution input incomplete")
	r := PredicateRuntime{LocalBoundsOnly: true, Expansion: predicateExpansionFunc(func(context.Context, commandsurface.DeferredWord) (PredicateValue, error) { return nil, cause })}
	_, err := r.TestExpression(t.Context(), fixtureInvocation(), commandsurface.TestExpression{Expression: commandsurface.UnaryPredicate{Operator: "!", Operand: predicateWord("ignored")}})
	if !errors.Is(err, cause) {
		t.Fatalf("expansion stop erased %v", err)
	}
}
func TestTypedPredicateUsesSelectedTreeDepthGuard(t *testing.T) {
	var node commandsurface.TestNode = predicateWord("x")
	for range predicateBounds().depth {
		node = commandsurface.GroupedPredicate{Expression: node}
	}
	_, err := (PredicateRuntime{LocalBoundsOnly: true}).TestExpression(t.Context(), fixtureInvocation(), commandsurface.TestExpression{Expression: node})
	var limit *PredicateLimitError
	if !errors.As(err, &limit) || limit.Resource != "ast_depth" {
		t.Fatalf("malformed detached tree unbounded %v", err)
	}
}

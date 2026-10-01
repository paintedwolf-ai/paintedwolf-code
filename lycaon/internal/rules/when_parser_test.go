package rules

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWhenParserParenthesesOverridePrecedence(t *testing.T) {
	node, err := boolexpr.Parse("(a or b) and c")
	testutil.FailErr(t, "boolexpr.Parse failed", err)
	env := map[string]bool{"a": false, "b": true, "c": true}
	got := boolexpr.Eval(node, func(name string) bool { return env[name] })
	if !got {
		t.Fatal("expected (a or b) and c to be true")
	}
	env["b"] = false
	got = boolexpr.Eval(node, func(name string) bool { return env[name] })
	if got {
		t.Fatal("expected false when b is false and a is false")
	}
}

func TestWhenParserAndOrNotPrecedence(t *testing.T) {
	node, err := boolexpr.Parse("a and b or c")
	testutil.FailErr(t, "boolexpr.Parse failed", err)
	env := map[string]bool{"a": false, "b": true, "c": true}
	got := boolexpr.Eval(node, func(name string) bool { return env[name] })
	if !got {
		t.Fatal("expected (a and b) or c to be true")
	}
}

func TestWhenParserUnknownIdentifierFailsAtEval(t *testing.T) {
	reg := conditions.NewRegistry()
	node, err := boolexpr.Parse("unknown_leaf")
	testutil.FailErr(t, "boolexpr.Parse failed", err)
	ok, err := reg.Evaluate("unknown_leaf", conditions.EvalContext{})
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_ = boolexpr.Eval(node, func(name string) bool {
		v, _ := reg.Evaluate(name, conditions.EvalContext{})
		return v
	})
}

func TestMatchWhenExprViaRegistry(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("leaf_a", func(_ conditions.EvalContext) (bool, error) { return true, nil })
	ok, err := MatchWhenExpr(reg, "leaf_a and leaf_a", EvalContext{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

// A when map with one falsy key is the negation of that condition, and its
// canonical form carries no operator to scan for.
func TestMatchWhenSingleFalsyKeyNegates(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("leaf_a", func(_ conditions.EvalContext) (bool, error) { return true, nil })
	_ = reg.Register("leaf_b", func(_ conditions.EvalContext) (bool, error) { return false, nil })

	ok, err := MatchWhen(reg, map[string]any{"leaf_a": false}, EvalContext{})
	if err != nil {
		t.Fatalf("negated true condition errored: %v", err)
	}
	if ok {
		t.Fatal("when {leaf_a: false} must not match while leaf_a holds")
	}

	ok, err = MatchWhen(reg, map[string]any{"leaf_b": false}, EvalContext{})
	if err != nil {
		t.Fatalf("negated false condition errored: %v", err)
	}
	if !ok {
		t.Fatal("when {leaf_b: false} must match while leaf_b is false")
	}
}

// A parameterized id carries characters no expression grammar bounds, so the
// map form must never round-trip through rendered text.
func TestMatchWhenParameterizedIdSurvivesNegation(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.RegisterParameterized("var_equals:", func(_ conditions.EvalContext) (bool, error) {
		return false, nil
	})
	ok, err := MatchWhen(reg, map[string]any{"var_equals:plan.status,draft": false}, EvalContext{})
	if err != nil {
		t.Fatalf("parameterized negation errored: %v", err)
	}
	if !ok {
		t.Fatal("negation of a false parameterized condition must match")
	}
}

// An unregistered condition can never match, so it is an authoring error.
func TestMatchWhenUnknownConditionIsAnError(t *testing.T) {
	reg := conditions.NewRegistry()
	if _, err := MatchWhen(reg, map[string]any{"nope": true}, EvalContext{}); err == nil {
		t.Fatal("unknown condition must surface as an error")
	}
	if err := ValidateWhen(reg, map[string]any{"nope": true}); err == nil {
		t.Fatal("validation must reject an unknown condition")
	}
}

// A registered condition that fails to run is operational, not an authoring
// error: the leaf reads false and the tool call is not rejected.
func TestMatchWhenOperationalConditionErrorDoesNotReject(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("flaky", func(_ conditions.EvalContext) (bool, error) {
		return false, errNotAvailable
	})
	ok, err := MatchWhen(reg, map[string]any{"flaky": true}, EvalContext{})
	if err != nil {
		t.Fatalf("operational failure must not become a rule error: %v", err)
	}
	if ok {
		t.Fatal("an unreadable condition must not match")
	}
}

var errNotAvailable = errors.New("fact not available")

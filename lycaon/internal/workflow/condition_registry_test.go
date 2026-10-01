package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
)

func TestConditionRegistryRegisterAndEvaluate(t *testing.T) {
	reg := conditions.NewRegistry()
	if err := reg.Register("always_true", func(_ conditions.EvalContext) (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := reg.Evaluate("always_true", conditions.EvalContext{})
	if err != nil || !ok {
		t.Fatalf("evaluate = %v err = %v", ok, err)
	}
}

func TestConditionRegistryDuplicateRegister(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("dup", func(_ conditions.EvalContext) (bool, error) { return true, nil })
	err := reg.Register("dup", func(_ conditions.EvalContext) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestConditionRegistryUnknownFailsClosed(t *testing.T) {
	reg := conditions.NewRegistry()
	ok, err := reg.Evaluate("missing", conditions.EvalContext{})
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !conditions.IsUnknownCondition(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestConditionRegistryParameterized(t *testing.T) {
	reg := conditions.NewRegistry()
	if err := reg.RegisterParameterized("evidence_passed:", func(ctx conditions.EvalContext) (bool, error) {
		return ctx.Phase == "verify", nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reg.Has("evidence_passed:verify") {
		t.Fatal("expected parameterized registration")
	}
	ok, err := reg.Evaluate("evidence_passed:verify", conditions.EvalContext{Phase: "verify"})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

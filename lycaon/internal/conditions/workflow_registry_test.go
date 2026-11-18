package conditions_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConditionRegistryRegisterAndEvaluate(t *testing.T) {
	reg := conditions.NewRegistry()
	if err := reg.Register("workflow_active", func(_ conditions.EvalContext) (bool, error) {
		return true, nil
	}); err != nil {
		testutil.FailErr(t, "reg.Register failed", err)
	}
	ok, err := reg.Evaluate("workflow_active", conditions.EvalContext{})
	if err != nil || !ok {
		t.Fatalf("evaluate = %v err = %v", ok, err)
	}
}

func TestConditionRegistryDuplicateRegister(t *testing.T) {
	reg := conditions.NewRegistry()
	_ = reg.Register("workflow_paused", func(_ conditions.EvalContext) (bool, error) { return true, nil })
	err := reg.Register("workflow_paused", func(_ conditions.EvalContext) (bool, error) { return false, nil })
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
		testutil.FailErr(t, "reg.RegisterParameterized failed", err)
	}
	if !reg.Has("evidence_passed:verify") {
		t.Fatal("expected parameterized registration")
	}
	ok, err := reg.Evaluate("evidence_passed:verify", conditions.EvalContext{Phase: "verify"})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

package conditions

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVarEqualsDotPath(t *testing.T) {
	reg, err := NewDefaultRegistry(RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry failed", err)
	vars := SetDotPath(nil, "plan.status", "approved")
	ok, err := reg.Evaluate("var_equals:plan.status,approved", EvalContext{Vars: vars})
	if err != nil || !ok {
		t.Fatalf("var_equals approved = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("var_equals:plan.status,draft", EvalContext{Vars: vars})
	if err != nil || ok {
		t.Fatalf("var_equals draft = %v err=%v", ok, err)
	}
}

func TestVarTruthyDotPath(t *testing.T) {
	reg, err := NewDefaultRegistry(RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry failed", err)
	vars := SetDotPath(nil, "phase_skipped.research", true)
	ok, err := reg.Evaluate("var_truthy:phase_skipped.research", EvalContext{Vars: vars})
	if err != nil || !ok {
		t.Fatalf("var_truthy = %v err=%v", ok, err)
	}
}

func TestHumanApprovalIsRegistered(t *testing.T) {
	reg, err := NewDefaultRegistry(RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry failed", err)
	if !reg.Has("human_approval") {
		t.Fatal("human_approval must be registered")
	}
}

func TestIsHostOnlyCondition(t *testing.T) {
	if !IsHostOnlyCondition("var_set:plan.status") {
		t.Fatal("var_set must be host-only")
	}
	if IsHostOnlyCondition("var_equals:plan.status,approved") {
		t.Fatal("var_equals is readable in rules")
	}
}

package conditions

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
)

func TestRegisterRejectsForbiddenID(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register("stage_plan_complete", func(_ EvalContext) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected forbidden Register error")
	}
	if !errors.Is(err, ErrForbiddenCondition) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterAllowsParameterizedEvidence(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterParameterized("evidence_passed:", func(_ EvalContext) (bool, error) {
		return true, nil
	}); err != nil {
		testutil.FailErr(t, "reg.RegisterParameterized failed", err)
	}
	ok, err := reg.Evaluate("evidence_passed:verify", EvalContext{})
	if err != nil || !ok {
		t.Fatalf("evidence_passed:verify = %v err=%v", ok, err)
	}
}

func TestRegisterRejectsForbiddenPattern(t *testing.T) {
	reg := NewRegistry()
	cases := []string{
		"stage_foo_complete",
		"verify_evidence_passed",
		"user_input_clarify",
		"mode_is",
	}
	for _, id := range cases {
		err := reg.Register(id, func(_ EvalContext) (bool, error) { return false, nil })
		if err == nil || !errors.Is(err, ErrForbiddenCondition) {
			t.Fatalf("Register(%q) = %v want forbidden", id, err)
		}
	}
}

func TestEvaluateRejectsForbiddenID(t *testing.T) {
	reg := NewRegistry()
	ok, err := reg.Evaluate("stage_plan_complete", EvalContext{})
	if err == nil || !errors.Is(err, ErrForbiddenCondition) {
		t.Fatalf("Evaluate forbidden = ok=%v err=%v", ok, err)
	}
}

func TestRegisterAllowsShippedID(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("plan_stub_valid", func(_ EvalContext) (bool, error) { return true, nil }); err != nil {
		testutil.FailErr(t, "reg.Register failed", err)
	}
}

func TestIsCatalogStub(t *testing.T) {
	if !vocabulary.IsCatalogOnly("findings_triaged") {
		t.Fatal("expected scan catalog stub")
	}
	if vocabulary.IsCatalogOnly("phase_skipped:research") {
		t.Fatal("phase_skipped is shipped core")
	}
	if vocabulary.IsCatalogOnly("var_equals:plan.status,approved") {
		t.Fatal("var_equals is shipped core")
	}
	if vocabulary.IsCatalogOnly("topology_stage_complete") {
		t.Fatal("topology_stage_complete is shipped core")
	}
	if vocabulary.IsCatalogOnly("unknown_predicate") {
		t.Fatal("unknown predicate must not be a catalog stub")
	}
	if vocabulary.IsCatalogOnly("human_approval") {
		t.Fatal("human_approval is shipped")
	}
}

package rules

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMatchWhenVarEquals(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	vars := conditions.SetDotPath(nil, "plan.status", "draft")
	ok, err := MatchWhen(reg, map[string]any{
		"var_equals:plan.status,draft": true,
	}, EvalContext{EvalContext: conditions.EvalContext{Vars: vars}})
	if err != nil || !ok {
		t.Fatalf("match = %v err=%v", ok, err)
	}
}

func TestValidateRejectsVarSetInWhenKey(t *testing.T) {
	if !conditions.IsHostOnlyCondition("var_set:plan.status") {
		t.Fatal("var_set must be host-only")
	}
}

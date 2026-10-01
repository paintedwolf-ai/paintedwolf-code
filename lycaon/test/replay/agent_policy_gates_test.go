package replay_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAdvisoryProseDoesNotSatisfyGateLeaf(t *testing.T) {
	vars := map[string]any{
		"advisory_summary_text": "Advisory summary recommends merging this approach.",
		"gates": map[string]any{
			"verify": false,
		},
	}
	ec := conditions.EvalContext{
		ConditionID: "gate_passed:verify",
		Vars:        vars,
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("gate_passed:verify", ec)
	testutil.FailErr(t, "reg.Evaluate failed", err)
	if ok {
		t.Fatal("advisory prose vars must not satisfy gate_passed:verify")
	}
}

func TestAdvisoryProseDoesNotSatisfyEvidenceVerify(t *testing.T) {
	ec := conditions.EvalContext{
		ConditionID: "evidence_passed:verify",
		ProjectDir:  t.TempDir(),
		Vars: map[string]any{
			"advisory_summary_text": "Summary claims tests passed without anchors.",
		},
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("evidence_passed:verify", ec)
	testutil.FailErr(t, "reg.Evaluate failed", err)
	if ok {
		t.Fatal("advisory prose must not satisfy evidence_passed:verify without JSONL anchors")
	}
	_ = evidence.GateTypeVerify
}

package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHintDeprecatedSuccessorValidated(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	entry, ok := cfg.HintCodes["WRITE_SCOPE_DENIED"]
	if !ok {
		t.Fatal("missing WRITE_SCOPE_DENIED")
	}
	entry.Status = "deprecated"
	entry.Related = []guidance.HintRelated{{ID: "WRITE_BINARY_DENIED", Type: "obsolete"}}
	cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
	testutil.FailErr(t, "ValidateHintConfig", guidance.ValidateHintConfig(cfg))
}

func TestHintSuccessorUnknownRejected(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	entry := cfg.HintCodes["WRITE_SCOPE_DENIED"]
	entry.Status = "deprecated"
	entry.Successor = "approval gate agent_policy_change"
	entry.Related = []guidance.HintRelated{{ID: "NOT_A_REAL_HINT_CODE", Type: "obsolete"}}
	cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
	if err := guidance.ValidateHintConfig(cfg); err == nil {
		t.Fatal("expected unknown successor to fail validation")
	}
}

func TestHintDeprecatedSuccessorRequired(t *testing.T) {
	for _, relations := range [][]guidance.HintRelated{
		nil,
		{{ID: "WRITE_SCOPE_DENIED", Type: "obsolete"}},
		{{ID: "WRITE_BINARY_DENIED", Type: "similar"}},
	} {
		cfg, err := guidance.LoadHintConfigStock()
		testutil.FailErr(t, "load hint registry", err)
		entry := cfg.HintCodes["WRITE_SCOPE_DENIED"]
		entry.Status = "deprecated"
		entry.Successor = " "
		entry.Related = relations
		cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
		if err := guidance.ValidateHintConfig(cfg); err == nil {
			t.Errorf("expected invalid successor relations %v to fail", relations)
		}
	}
}

func TestHintBoundarySuccessorValidated(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	entry := cfg.HintCodes["WRITE_SCOPE_DENIED"]
	entry.Status = "deprecated"
	entry.Successor = "approval gate agent_policy_change"
	cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
	testutil.FailErr(t, "validate boundary retirement", guidance.ValidateHintConfig(cfg))
}

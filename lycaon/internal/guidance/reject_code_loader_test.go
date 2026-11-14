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
	entry.Successor = "WRITE_BINARY_DENIED"
	cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
	testutil.FailErr(t, "ValidateHintConfig", guidance.ValidateHintConfig(cfg))
}

func TestHintSuccessorUnknownRejected(t *testing.T) {
	for _, successor := range []string{"NOT_A_REAL_HINT_CODE", "WRITE_SCOPE_DENIED"} {
		cfg, err := guidance.LoadHintConfigStock()
		testutil.FailErr(t, "LoadHintConfigStock", err)
		entry := cfg.HintCodes["WRITE_SCOPE_DENIED"]
		entry.Status = "deprecated"
		entry.Successor = successor
		cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
		if err := guidance.ValidateHintConfig(cfg); err == nil {
			t.Errorf("successor %s was accepted", successor)
		}
	}
}

func TestHintDeprecatedSuccessorRequired(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	entry := cfg.HintCodes["WRITE_SCOPE_DENIED"]
	entry.Status = "deprecated"
	entry.Successor = " "
	cfg.HintCodes["WRITE_SCOPE_DENIED"] = entry
	if err := guidance.ValidateHintConfig(cfg); err == nil {
		t.Error("deprecated code without a successor was accepted")
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

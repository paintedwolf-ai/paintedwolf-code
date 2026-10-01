package oar

import (
	"testing"
)

func TestValidateConditionFactValuesValid(t *testing.T) {
	t.Parallel()
	codes := map[string]struct{}{"GREP_PATH_NOT_FOUND": {}}
	cases := []string{
		`paintedwolf.batch_phase == "synthesize"`,
		`paintedwolf.batch_phase in ["dispatch", "integrate"]`,
		`paintedwolf.surface == "implement_synthesis"`,
		`paintedwolf.scope_mode == "read"`,
		`paintedwolf.phase == "plan"`,
		`paintedwolf.promote_order == "sequential"`,
		`paintedwolf.status == "running"`,
		`paintedwolf.rejection_code == "GREP_PATH_NOT_FOUND"`,
	}
	for _, expr := range cases {
		if err := ValidateConditionFactValues(expr, codes); err != nil {
			t.Errorf("expected valid for %q, got: %v", expr, err)
		}
	}
}

func TestValidateConditionFactValuesInvalid(t *testing.T) {
	t.Parallel()
	codes := map[string]struct{}{"GREP_PATH_NOT_FOUND": {}}
	cases := []string{
		`paintedwolf.batch_phase == "bogus_phase"`,
		`paintedwolf.surface == "non_existent_surface"`,
		`paintedwolf.scope_mode == "execute"`,
		`paintedwolf.promote_order in ["independent", "random"]`,
		`paintedwolf.rejection_code == "NOT_A_CODE"`,
	}
	for _, expr := range cases {
		if err := ValidateConditionFactValues(expr, codes); err == nil {
			t.Errorf("expected error for %q, got nil", expr)
		}
	}
}

func TestValidateConditionFactValuesOpenRejectionCodes(t *testing.T) {
	t.Parallel()
	if err := ValidateConditionFactValues(`paintedwolf.rejection_code == "ANY_CODE"`, nil); err != nil {
		t.Fatalf("rejection_code without a vocabulary should accept any literal: %v", err)
	}
}

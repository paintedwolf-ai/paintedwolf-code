package parse

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseValidateRejectsInvalidJSON(t *testing.T) {
	svc := NewDefaultService()
	out, err := svc.Validate(context.Background(), "not json", nil)
	testutil.FailErr(t, "svc.Validate failed", err)
	if out.Valid {
		t.Fatal("expected invalid JSON")
	}
}

func TestParseExtractJSONFromFence(t *testing.T) {
	svc := NewDefaultService()
	out, err := svc.ExtractJSON(context.Background(), "text\n```json\n{\"a\":1}\n```", nil)
	testutil.FailErr(t, "svc.ExtractJSON failed", err)
	if !out.Extracted {
		t.Fatal("expected extracted JSON")
	}
}

func TestParseDelegationPlanRequiresTask(t *testing.T) {
	svc := NewDefaultService()
	_, err := svc.ValidateDelegationPlan(context.Background(), `{"legs":[{"title":"x"}]}`)
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

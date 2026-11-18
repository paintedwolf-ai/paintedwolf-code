package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateBootToolClaimsHonest(t *testing.T) {
	reg := NewDefaultRegistry()
	for _, name := range bootToolClaimNames {
		if err := reg.Register(name, func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
			return "ok", nil
		}); err != nil {
			testutil.FailErr(t, "reg.Register failed", err)
		}
	}
	if err := ValidateBootToolClaimsHonest(reg); err != nil {
		testutil.FailErr(t, "ValidateBootToolClaimsHonest failed", err)
	}
	reg2 := NewDefaultRegistry()
	if err := ValidateBootToolClaimsHonest(reg2); err == nil {
		t.Fatal("expected missing claims error")
	}
}

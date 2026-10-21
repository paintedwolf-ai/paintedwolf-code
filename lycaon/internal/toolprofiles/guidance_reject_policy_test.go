package toolprofiles

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// The host wraps its profile engine in the guidance policy; command programs
// still freeze the wrapped profile's effect boundary through it.
func TestGuidancePolicyFreezesTheWrappedCommandProfile(t *testing.T) {
	readonly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: readonly}})
	_, effects, err := NewGuidanceRejectPolicy(NewProfilePolicyEngine(boundary)).FreezeCommandProfile(t.Context(), "fixture")
	testutil.FailErr(t, "freeze wrapped command profile", err)
	if effects != readonly {
		t.Fatalf("frozen effects = %v, want %v", effects, readonly)
	}
	if _, _, err := NewGuidanceRejectPolicy(nil).FreezeCommandProfile(t.Context(), "fixture"); err == nil {
		t.Fatal("a wrapped engine without a command boundary admitted a program")
	}
}

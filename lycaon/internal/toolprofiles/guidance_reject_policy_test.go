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
	inner := NewProfilePolicyEngine(boundary)
	for _, command := range []string{"cat", "rm"} {
		want, wantErr := inner.CommandOperationAllowed(t.Context(), "fixture", "read", command)
		got, err := NewGuidanceRejectPolicy(inner).CommandOperationAllowed(t.Context(), "fixture", "read", command)
		if got != want || (err == nil) != (wantErr == nil) {
			t.Fatalf("%s visibility = %t %v, wrapped engine %t %v", command, got, err, want, wantErr)
		}
	}
	if _, err := NewGuidanceRejectPolicy(nil).CommandOperationAllowed(t.Context(), "fixture", "read", "cat"); err == nil {
		t.Fatal("a wrapped engine without native visibility exposed an operation")
	}
}

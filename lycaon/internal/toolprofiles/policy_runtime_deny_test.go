package toolprofiles_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProfilePolicyEngineRuntimeDenyWebSearch(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"web_search": true, "fetch_url": true},
	}})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	policy.SetRuntimeToolDeny(func(toolName string) bool {
		return toolName == "web_search" || toolName == "fetch_url"
	})
	decision, err := policy.Evaluate(context.Background(), platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "web_search",
	})
	testutil.FailErr(t, "policy.Evaluate failed", err)
	if !decision.Blocked {
		t.Fatalf("decision = %+v", decision)
	}
	fetchDecision, err := policy.Evaluate(context.Background(), platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "fetch_url",
	})
	testutil.FailErr(t, "policy.Evaluate failed", err)
	if !fetchDecision.Blocked {
		t.Fatalf("fetch_url decision = %+v", fetchDecision)
	}
}

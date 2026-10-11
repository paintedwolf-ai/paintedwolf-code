//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestListedToolsPassRulesEvalWithEmptyArgs(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	for _, meta := range listed {
		if err := fix.Mgr.Coordinator.Guards.Policy().EvaluateInvoke(ctx, fix.Sess, meta.Name, nil); err != nil {
			t.Fatalf("listed tool %q failed rules eval: %v", meta.Name, err)
		}
	}
}

func TestSpecPromptToolCountLessThanBuild(t *testing.T) {
	specFix := setupContextualToolsFixture(t, api.SessionPostureSpec)
	buildFix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	specCount := len(specFix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, specFix.Sess, specFix.ProfileID))
	buildCount := len(buildFix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, buildFix.Sess, buildFix.ProfileID))
	if specCount >= buildCount {
		t.Fatalf("spec tool count = %d want < build %d", specCount, buildCount)
	}
}

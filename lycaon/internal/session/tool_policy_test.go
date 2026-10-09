package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestListToolsForPromptSpecHidesDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureSpec)
	ctx := context.Background()
	listed := fix.Mgr.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	if hasTool(listed, "delegate_dispatch") {
		t.Fatalf("spec posture must hide delegate_dispatch, got %v", toolNames(listed))
	}
	if !hasTool(listed, "read") {
		t.Fatal("expected read visible in spec posture")
	}
	if !hasTool(listed, "workflow_compose") {
		t.Fatal("expected workflow_compose visible in spec posture")
	}
}

func TestListToolsForPromptBuildShowsDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	if !hasTool(listed, "delegate_dispatch") {
		t.Fatalf("build posture must show delegate_dispatch, got %v", toolNames(listed))
	}
	if !hasTool(listed, "task") {
		t.Fatal("expected task visible in build posture")
	}
}

func TestListToolsForPromptCoordinatorCeiling(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	// Profile grant; the turn surface gates invoke.
	for _, name := range []string{"grep", "read", "command"} {
		if !hasTool(listed, name) {
			t.Fatalf("coordinator profile should list %q", name)
		}
	}
	if !hasTool(listed, "write") {
		t.Fatal("coordinator profile should list write (plan-scoped)")
	}
}

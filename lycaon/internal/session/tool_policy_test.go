package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestListToolsForPromptSpecHidesDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureSpec)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	if hasTool(listed, "delegate_dispatch") {
		t.Fatalf("spec posture must hide delegate_dispatch, got %v", toolNames(listed))
	}
	assertPromptCommand(t, listed)
	if !hasTool(listed, "workflow_compose") {
		t.Fatal("expected workflow_compose visible in spec posture")
	}
	surface, err := fix.Executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: fix.ProfileID, SurfaceID: "observe_plan"}, nil)
	testutil.FailErr(t, "capture read-only planning surface", err)
	for _, class := range []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress} {
		effects, err := toolcontract.NewCommandEffects(class)
		testutil.FailErr(t, "declare command effect", err)
		if surface.AllowsEffects(effects) != (class == toolcontract.CommandReadOnly) {
			t.Fatalf("planning surface has incorrect effect restriction for %q", class)
		}
	}
}

func TestListToolsForPromptBuildShowsDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
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
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	assertPromptCommand(t, listed)
	for _, name := range []string{"delegate_dispatch", "task"} {
		if !hasTool(listed, name) {
			t.Fatalf("coordinator profile should list %q", name)
		}
	}
	surface, err := fix.Executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: fix.ProfileID, SurfaceID: "implement_investigate"}, nil)
	testutil.FailErr(t, "capture coordinator investigation surface", err)
	for _, class := range []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress} {
		effects, err := toolcontract.NewCommandEffects(class)
		testutil.FailErr(t, "declare command effect", err)
		if !surface.AllowsEffects(effects) {
			t.Fatalf("coordinator investigation should allow effect %q", class)
		}
	}
}

func assertPromptCommand(t *testing.T, listed []tools.ToolMeta) {
	t.Helper()
	for _, meta := range listed {
		if meta.Name != "command" {
			continue
		}
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		source, _ := properties["command"].(map[string]any)
		if meta.ArgsSchema["type"] != "object" || source["type"] != "string" {
			t.Fatal("command discovery must expose its structured source argument")
		}
		contract, ok := toolcontract.Lookup(meta.Name)
		if !ok || contract.Owner != "processes" || contract.Lifecycle != toolcontract.LifecycleEffectAttempt {
			t.Fatalf("command discovery has incorrect execution contract: %+v", contract)
		}
		return
	}
	t.Fatalf("expected command discovery, got %v", toolNames(listed))
}

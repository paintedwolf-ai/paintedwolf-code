//go:build integration

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestComposeProposalHumanStartAdvanceToolPath(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint config", err)
	if err := RegisterStateTools(reg, StateToolDeps{Runs: mgr, Sessions: mgr.Sessions}); err != nil {
		testutil.FailErr(t, "RegisterStateTools failed", err)
	}
	if err := RegisterAdvanceTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterAdvanceTool failed", err)
	}
	ctx := context.Background()
	tctx := toolContext("coordinator", "sess-1", projectDir)

	_, err = reg.Run(ctx, "state_start", map[string]any{
		"workflow_id": "plan", "workflow_version": "1.0.0",
	}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_START_REQUIRES_HUMAN_APPROVAL" {
		t.Fatalf("state_start err = %v, want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL reject", err)
	}
	rendered := toolrejection.RenderReject(&toolrejection.ToolReject{Code: reject.Code, Data: map[string]any{"tool": "state_start"}}, guidance.NewStaticRejectFormatter(hints))
	for _, want := range []string{"Rejected:", "Code: WORKFLOW_START_REQUIRES_HUMAN_APPROVAL", "Wait for the user"} {
		if !strings.Contains(rendered.Error(), want) {
			t.Fatalf("rendered reject missing %q:\n%s", want, rendered)
		}
	}
	_, err = mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	run, err := mgr.GetActive(ctx, "sess-1")
	if err != nil || run == nil {
		t.Fatal("expected active run")
	}
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)

	advanced, err := mgr.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "mgr.TryAutoAdvance failed", err)
	if advanced.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", advanced.CurrentPhase)
	}
}

func TestAdvanceToolMatchesHTTPGateShape(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterStateTools(reg, StateToolDeps{Runs: mgr, Sessions: mgr.Sessions}); err != nil {
		testutil.FailErr(t, "RegisterStateTools failed", err)
	}
	if err := RegisterAdvanceTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterAdvanceTool failed", err)
	}
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "gate-shape",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "stub",
			CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
			Gates:              []string{"plan_stub_valid"},
			Next:               "next",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
		}, {ID: "next"}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"gate-shape@1.0.0": manifest})
	if _, err := startRun(ctx, mgr, "sess-1", "gate-shape", "1.0.0"); err != nil {
		testutil.FailErr(t, "startRun failed", err)
	}
	tctx := toolContext("coordinator", "sess-1", projectDir)

	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, tctx)
	testutil.FailErr(t, "reg.Run failed", err)
	var toolResult AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &toolResult); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	run, err := mgr.GetActive(ctx, "sess-1")
	if err != nil || run == nil {
		t.Fatal(err)
	}
	_, httpErr := mgr.Advance(ctx, run.ID)
	gateErr, ok := IsPhaseGateUnmet(httpErr)
	if !ok {
		t.Fatalf("http err = %v", httpErr)
	}
	if toolResult.Error != "phase_gate_unmet" {
		t.Fatalf("tool error = %q", toolResult.Error)
	}
	if toolResult.Phase != gateErr.Phase {
		t.Fatalf("tool phase %q http phase %q", toolResult.Phase, gateErr.Phase)
	}
	if len(toolResult.FailedLeaves) != len(gateErr.FailedLeaves) {
		t.Fatalf("tool leaves %v http leaves %v", toolResult.FailedLeaves, gateErr.FailedLeaves)
	}
}

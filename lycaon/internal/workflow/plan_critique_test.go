package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanCritiqueLeaveSurvivesFinalize(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	review, ok := m.PhaseByID("review")
	if !ok {
		t.Fatal("review must survive FinalizeManifest")
	}
	if review.ReviewLoop == nil || strings.TrimSpace(review.ReviewLoop.EvidenceKey) != "plan_review" {
		t.Fatalf("review_loop = %+v", review.ReviewLoop)
	}
	for _, id := range m.Phases {
		if id == "review" {
			t.Fatal("review must stay off next-only Phases spine")
		}
	}
	approve, ok := m.PhaseByID("approve")
	if !ok {
		t.Fatal("missing approve")
	}
	edge, ok := approve.TransitionByID("critique")
	if !ok || edge.To != "review" {
		t.Fatalf("critique edge = %+v", edge)
	}
	expand, _ := m.PhaseByID("expand")
	if expand.Next != "approve" {
		t.Fatalf("expand.next = %q want approve", expand.Next)
	}
	if approve.HumanApproval == nil || strings.TrimSpace(approve.HumanApproval.Readiness) != "plan_stub_valid" {
		t.Fatalf("approve readiness = %+v", approve.HumanApproval)
	}
}

func TestPlanCritiqueFireFromApprove(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	testutil.FailErr(t, "StartHuman", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanThroughExpand", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	ui, err := mgr.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui == nil || !ui.HumanApprovalAwaiting {
		t.Fatal("expected human_approval_awaiting before critique")
	}
	if len(ui.ChoiceTransitions) != 1 || ui.ChoiceTransitions[0].ID != "critique" {
		t.Fatalf("choice_transitions = %+v", ui.ChoiceTransitions)
	}

	out, err := mgr.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition critique", err)
	if out.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", out.CurrentPhase)
	}

	// Off the approve phase the Approve chrome drops: the human_approval.*
	// vars survive the critique leave, but awaiting follows the phase shape.
	ui, err = mgr.ComputeRunUI(ctx, out)
	testutil.FailErr(t, "ComputeRunUI in review", err)
	if ui == nil || ui.HumanApprovalAwaiting {
		t.Fatal("human_approval_awaiting must be false while the run is in review")
	}
}

func TestPlanReviewPhaseSkippedLeafNotInApproveReadiness(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	for _, rel := range []string{
		"config/packs/painted-wolf/plan/workflows/plan/workflow.yaml",
		"config/packs/painted-wolf/platform/host/posture-rules",
		"config/packs/painted-wolf/platform/guidance/gate-feedback",
	} {
		_ = filepath.Walk(filepath.Join(root, rel), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if strings.Contains(string(raw), "plan_stub_valid and (phase_skipped:review") {
				hits = append(hits, path)
			}
			return nil
		})
	}
	if len(hits) > 0 {
		t.Fatalf("approve readiness must not OR phase_skipped:review: %v", hits)
	}
}

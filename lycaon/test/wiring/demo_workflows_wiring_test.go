package wiring

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecuritySurveyFanOutWorkflowEndToEnd(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "security-survey")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	run, err := h.WorkflowMgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "1.0.1"})
	testutil.FailErr(t, "start security patch workflow", err)
	settleScanObligationAndAdvance(t, h, ctx, run.ID, "plan")
	satisfyFanoutPlannedAndAdvance(t, h, ctx, run.ID, dir, []workflow.FanoutPlanLeg{
		{AgentType: "security-reviewer", Subject: "Dependencies", Prompt: "Survey dependency risk"},
		{AgentType: "security-reviewer", Subject: "Sign-in", Prompt: "Survey auth patterns"},
	}, "execute")
	satisfyWorkerCycleAndAdvance(t, h, ctx, sess, run.ID, dir)
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "claims")

	assertManifestBoundSurface(t, h, ctx, sess, surface.HostLoopWakeSentinel, "review_adjudicate")

	scaffoldTopologyOutput(t, h.WorkflowMgr, run.ID, orchestration.TopologyBindStageFanOut)

	h.SessionMgr.ClearPendingKickForTest(sess.ID)

	claimed := map[string]string{
		"verdict":      "CLAIMED",
		"set_asides":   "[]",
		"threat_model": "HTTP service; unauthenticated clients on the public internet; session cookie is the auth boundary",
		"claims":       `[{"id":"c1","title":"Request id reaches a formatted SQL query","status":"claimed","statement":"SQLi in internal/store/query.go:88 — attacker-controlled id reaches Sprintf","cited_evidence":[{"path":"internal/store/query.go","line":88,"excerpt":"id reaches Sprintf in query.go:88"}]}]`,
	}
	// Coverage citations must resolve through the production grounding floor.
	uncited := map[string]string{"verdict": "CLAIMED", "set_asides": "[]", "threat_model": claimed["threat_model"], "claims": `[{"id":"c1","title":"SQL injection","status":"claimed","statement":"SQLi"}]`}
	uncited["coverage"] = securityCoverageFixture(t, h, ctx, run.ID)
	out, err := h.WorkflowMgr.RecordReviewLoopVerdict(ctx, sess.ID, uncited, nil, nil)
	testutil.FailErr(t, "RecordReviewLoopVerdict uncited claims", err)
	if out.Valid || out.GroundingCode == "" {
		t.Fatalf("unresolved coverage citations outcome = %+v, want grounding refusal", out)
	}

	testutil.FailErr(t, "seed survey evidence", h.Store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "survey#1", Path: "internal/store/query.go",
		Kind: "read", Shape: evidence.ShapeFileRegion, SourceTool: "read", Fidelity: evidence.FidelityStructured,
		LineRanges: []evidence.LineRange{{Start: 88, End: 88}},
		Body:       []string{"id reaches Sprintf in query.go:88"},
	}))
	claimed["coverage"] = securityCoverageFixture(t, h, ctx, run.ID)
	out, err = h.WorkflowMgr.RecordReviewLoopVerdict(ctx, sess.ID, claimed, nil, nil)
	testutil.FailErr(t, "RecordReviewLoopVerdict claims", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("claims verdict = %+v, want valid terminal outcome", out)
	}
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "challenge")

	skepticChild := appendSucceededReviewAgent(t, h, ctx, sess, "skeptic")
	researcherChild := appendSucceededReviewAgent(t, h, ctx, sess, "web-researcher")

	// The challenge phase adjudicates each stamped claim by id, so the report
	// can say which survived rather than printing a lone verdict word.
	challenged := map[string]string{
		"verdict": "CHALLENGED",
		"challenges": `[{"id":"c1","status":"survives","statement":"the id still reaches Sprintf on every request",` +
			`"cited_evidence":[{"path":"internal/store/query.go","line":88,"excerpt":"id reaches Sprintf in query.go:88"}]}]`,
		"set_asides": `[]`,
	}
	challenged["coverage"] = securityCoverageFixture(t, h, ctx, run.ID)
	out, err = h.WorkflowMgr.RecordReviewLoopVerdict(ctx, sess.ID, challenged,
		[]api.CitationGroundingCitedEvidence{reviewerCitation(skepticChild, "skeptic"), reviewerCitation(researcherChild, "web-researcher")}, nil)
	testutil.FailErr(t, "RecordReviewLoopVerdict challenge", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("challenge verdict = %+v, want valid terminal outcome", out)
	}
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "report")

	deliverTopologyReport(t, h, ctx, sess, run.ID)
}

func TestReconPackFanOutWorkflowEndToEnd(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, "recon-pack")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureOrchestrate}, dir)
	testutil.FailErr(t, "create session", err)

	run := startWorkflowRunAt(t, h, ctx, sess, "recon-pack", "plan")
	satisfyFanoutPlannedAndAdvance(t, h, ctx, run.ID, dir, []workflow.FanoutPlanLeg{
		{AgentType: "path-explorer", Subject: "Packages", Prompt: "List top-level packages"},
		{AgentType: "path-explorer", Subject: "Test entrypoints", Prompt: "Find test entrypoints"},
	}, "execute")
	satisfyWorkerCycleAndAdvance(t, h, ctx, sess, run.ID, dir)
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "reconcile")
	_, err = h.WorkflowMgr.FireTransition(ctx, run.ID, "report", workflowdef.TransitionActorCoordinator)
	testutil.FailErr(t, "FireTransition report", err)
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "report")

	scaffoldTopologyOutput(t, h.WorkflowMgr, run.ID, orchestration.TopologyBindStageFanOut)

	deliverTopologyReport(t, h, ctx, sess, run.ID)
}

func TestBugbashWorkflowEndToEnd(t *testing.T) {
	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, context.Background())
	dir := h.ProjectDir(t, "bugbash")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureOrchestrate}, dir)
	testutil.FailErr(t, "create session", err)

	run := startTopologyWorkflowRun(t, h, ctx, sess, "bugbash")
	if run.CurrentPhase != "hunt" {
		t.Fatalf("phase = %q want hunt", run.CurrentPhase)
	}

	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races"} {
		waitTopologyStageComplete(t, h.WorkflowMgr, run.ID, stage)
	}
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "triage")
	waitTopologyStageComplete(t, h.WorkflowMgr, run.ID, "triage")
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "expand")
	blueprintPath := filepath.Join(dir, filepath.FromSlash(run.BlueprintPath))
	testutil.FailErr(t, "create Bugbash blueprint dir", os.MkdirAll(filepath.Dir(blueprintPath), 0o755))
	testutil.FailErr(t, "write Bugbash blueprint", os.WriteFile(blueprintPath, []byte(conditions.TestPlanContentStubOnly), 0o644))

	run, err = h.WorkflowMgr.Advance(ctx, run.ID)
	testutil.FailErr(t, "advance written fix plan", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	run, err = h.WorkflowMgr.SyncHumanApproval(ctx, run.ID, dir)
	testutil.FailErr(t, "SyncHumanApproval approve", err)

	child, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "get implementation child", err)
	if child == nil || child.WorkflowID != "implement" || child.ParentRunID == nil || *child.ParentRunID != run.ID {
		t.Fatalf("active run = %+v want implementation child of %s", child, run.ID)
	}
	if child.BlueprintPath != run.BlueprintPath {
		t.Fatalf("child blueprint = %q want %q", child.BlueprintPath, run.BlueprintPath)
	}
	now := time.Now().UTC()
	child.Status, child.CompletedAt, child.UpdatedAt = api.WorkflowRunStatusComplete, &now, now
	testutil.FailErr(t, "complete implementation child", h.WorkflowMgr.Store.Update(ctx, child))
	testutil.FailErr(t, "resume bugbash after implementation", h.WorkflowMgr.ReconcileTerminalRun(ctx, child))
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, run.ID, "closeout")

	vars, err := h.WorkflowMgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	stages, _ := vars["topology_stages"].(map[string]any)
	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races", "triage"} {
		entry, _ := stages[stage].(map[string]any)
		if entry == nil || entry["complete"] != true {
			t.Fatalf("topology_stages.%s = %v want complete", stage, entry)
		}
	}
}

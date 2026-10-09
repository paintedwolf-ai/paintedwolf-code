package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func reviewLoopTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "rltest",
		Version: "1.0.0",
		// Host auto-advances on gate satisfaction so RecordReviewLoopVerdict's TryAutoAdvance lands.
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "judge",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:rl_key"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:  "rl_key",
					IterationCap: 2,
					VerdictSchema: map[string]string{
						"verdict": "SELECTED|NEEDS_REVISION",
						"winner":  "string",
					},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func startReviewLoopRun(ctx context.Context, t *testing.T, mgr *RunManager) *api.WorkflowRun {
	t.Helper()
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": reviewLoopTestManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "startRun rltest", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge", run.CurrentPhase)
	}
	return run
}

func TestRecordReviewLoopVerdictPersistsAnchoredEvidence(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()

	hostDir := t.TempDir()
	mgr.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return hostDir, nil }

	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "rlpersist",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "claims",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:survey_claims"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:  "survey_claims",
					IterationCap: 1,
					VerdictSchema: map[string]string{
						"verdict":      "CLAIMED",
						"threat_model": "string",
						"claims":       "string",
					},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlpersist@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "rlpersist", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	cited := []api.CitationGroundingCitedEvidence{
		{Path: "internal/auth/auth.go", Line: 88, Excerpt: "db.Query"},
	}
	terminal := map[string]string{
		"verdict":      "CLAIMED",
		"threat_model": "HTTP service; unauthenticated clients",
		"claims":       "1. SQLi in auth.go:88",
	}
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", terminal, cited, nil); err != nil {
		testutil.FailErr(t, "terminal", err)
	}

	recs, err := mgr.ListReviewLoopEvidenceForType(ctx, "sess-1", run.ID, "claims", evidence.GateTypeSurveyClaims)
	testutil.FailErr(t, "ListReviewLoopEvidenceForType", err)
	if len(recs) != 1 {
		t.Fatalf("records = %d want 1", len(recs))
	}
	if recs[0].TypedGateVerdict() != evidence.GateVerdictApproved {
		t.Fatalf("verdict = %q want approved", recs[0].GateVerdict)
	}
	if got, _ := recs[0].Artifacts["claims"].(string); got != "1. SQLi in auth.go:88" {
		t.Fatalf("claims = %q", got)
	}
	ok, reason := inspector.EvidenceAnchored(recs[0])
	if !ok {
		t.Fatalf("EvidenceAnchored = false reason=%q", reason)
	}

	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", got.CurrentPhase)
	}
}

func TestRecordReviewLoopVerdictPersistsNonTerminal(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()

	hostDir := t.TempDir()
	mgr.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return hostDir, nil }
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": reviewLoopTestManifest()})
	run := startReviewLoopRun(ctx, t, mgr)

	cited := []api.CitationGroundingCitedEvidence{{Path: "a.go", Line: 1, Excerpt: "x"}}
	needs := map[string]string{"verdict": "NEEDS_REVISION", "winner": "undecided"}
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", needs, cited, nil); err != nil {
		testutil.FailErr(t, "needs", err)
	}

	recs, err := mgr.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
	testutil.FailErr(t, "ReadAll", err)
	if len(recs) != 1 {
		t.Fatalf("records = %d want 1 (non-terminal audit)", len(recs))
	}
	if recs[0].TypedGateVerdict() != evidence.GateVerdictNeedsChanges {
		t.Fatalf("verdict = %q want needs_changes", recs[0].GateVerdict)
	}
}

func TestRecordReviewLoopVerdictSkipsUngroundedAndInvalid(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()

	hostDir := t.TempDir()
	mgr.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return hostDir, nil }
	run := startReviewLoopRun(ctx, t, mgr)

	// Ungrounded (nil citations) → no record even when schema-valid.
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil); err != nil {
		testutil.FailErr(t, "ungrounded", err)
	}
	recs, err := mgr.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
	testutil.FailErr(t, "ReadAll after ungrounded", err)
	if len(recs) != 0 {
		t.Fatalf("ungrounded records = %d want 0", len(recs))
	}

	// Invalid → no record.
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "MAYBE", "winner": "B"},
		[]api.CitationGroundingCitedEvidence{{Path: "a.go", Line: 1, Excerpt: "x"}}, nil); err != nil {
		testutil.FailErr(t, "invalid", err)
	}
	recs, err = mgr.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
	testutil.FailErr(t, "ReadAll after invalid", err)
	if len(recs) != 0 {
		t.Fatalf("invalid records = %d want 0", len(recs))
	}
}

func TestRecordReviewLoopVerdictInvalidHolds(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	run := startReviewLoopRun(ctx, t, mgr)

	var heldCalls []bool
	mgr.OnReviewLoopHeld = func(_ context.Context, _ string, decisionRequired bool) {
		heldCalls = append(heldCalls, decisionRequired)
	}

	// An invalid verdict neither consumes a review round nor schedules a wake.
	verdict := map[string]string{"verdict": "MAYBE", "winner": "B"}
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", verdict, nil, nil); err != nil {
		testutil.FailErr(t, "RecordReviewLoopVerdict", err)
	}

	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge (invalid verdict must hold)", got.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := ReviewLoopAttempt(vars, "judge"); n != 0 {
		t.Fatalf("attempt = %d want 0 (invalid verdict is not a review round)", n)
	}
	if len(heldCalls) != 0 {
		t.Fatalf("held calls = %v want no continuation", heldCalls)
	}
}

func TestRecordReviewLoopVerdictIterationCap(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	run := startReviewLoopRun(ctx, t, mgr) // rltest judge: iteration_cap 2

	var heldCalls []bool
	mgr.OnReviewLoopHeld = func(_ context.Context, _ string, decisionRequired bool) {
		heldCalls = append(heldCalls, decisionRequired)
	}
	needsRevision := map[string]string{"verdict": "NEEDS_REVISION", "winner": "undecided"}

	// An available round keeps review open.
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil); err != nil {
		testutil.FailErr(t, "verdict 1", err)
	}
	// The final round requires a decision.
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil); err != nil {
		testutil.FailErr(t, "verdict 2", err)
	}
	// Further submissions retain the exhausted count and decision requirement.
	out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil)
	testutil.FailErr(t, "verdict 3", err)
	if out.Valid || !out.IterationCapExceeded || out.Attempt != 2 {
		t.Fatalf("outcome 3 = %+v want invalid+IterationCapExceeded at attempt 2", out)
	}

	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge (never advances on NEEDS_REVISION)", got.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := ReviewLoopAttempt(vars, "judge"); n != 2 {
		t.Fatalf("attempt = %d want 2 (rejected round must not bump past the cap)", n)
	}
	want := []bool{false, true, true} // continue, decision-required, decision-required again on the rejected round
	if len(heldCalls) != len(want) {
		t.Fatalf("held calls = %v want %v", heldCalls, want)
	}
	for i, w := range want {
		if heldCalls[i] != w {
			t.Fatalf("held calls = %v want %v", heldCalls, want)
		}
	}

	// A terminal verdict after the cap still decides it; the coordinator makes the choice.
	if _, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil); err != nil {
		testutil.FailErr(t, "terminal", err)
	}
	got, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after terminal", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done (terminal decision advances)", got.CurrentPhase)
	}
}

func TestRecordReviewLoopVerdictRequiresSucceededAgent(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "rlagents",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "judge",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:rl_key"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:    "rl_key",
					IterationCap:   2,
					RequiredAgents: []string{"skeptic"},
					VerdictSchema: map[string]string{
						"verdict": "SELECTED|NEEDS_REVISION",
						"winner":  "string",
					},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlagents@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "rlagents", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil)
	testutil.FailErr(t, "verdict without skeptic", err)
	if out.Valid || out.Terminal || len(out.MissingAgents) != 1 || out.MissingAgents[0] != "skeptic" {
		t.Fatalf("outcome = %+v want missing skeptic", out)
	}
	held, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if held.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge", held.CurrentPhase)
	}

	testutil.FailErr(t, "AppendMessages", mgr.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	out, err = mgr.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil)
	testutil.FailErr(t, "verdict with skeptic", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("outcome = %+v want terminal", out)
	}
	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after skeptic", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", got.CurrentPhase)
	}
}

func ifSpawnableReviewManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "rlspawn",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "challenge",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:survey_challenged"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:    "survey_challenged",
					IterationCap:   1,
					RequiredAgents: []string{"skeptic"},
					IfSpawnable:    []string{"web-researcher"},
					VerdictSchema:  map[string]string{"verdict": "CHALLENGED"},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func TestRecordReviewLoopVerdictIfSpawnableEmptySnapshot(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	mgr.ReviewSpawnFilter = func(_ context.Context, _, _ string, _ []string) []string {
		return nil
	}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	testutil.FailErr(t, "AppendMessages skeptic", mgr.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict with skeptic only", err)
	if !out.Valid || !out.Terminal || len(out.MissingAgents) != 0 {
		t.Fatalf("outcome = %+v want terminal without researcher", out)
	}
	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", got.CurrentPhase)
	}
}

func TestRecordReviewLoopVerdictIfSpawnableRequiresResearcher(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	testutil.FailErr(t, "AppendMessages skeptic", mgr.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict without researcher", err)
	if out.Valid || out.Terminal || len(out.MissingAgents) != 1 || out.MissingAgents[0] != "web-researcher" {
		t.Fatalf("outcome = %+v want missing web-researcher", out)
	}

	testutil.FailErr(t, "AppendMessages researcher", mgr.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-research",
			AgentType: "web-researcher",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}, {WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "web-researcher", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	out, err = mgr.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict with researcher", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("outcome = %+v want terminal", out)
	}
	got, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after researcher", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", got.CurrentPhase)
	}
}

func TestRecordReviewLoopVerdictRequiresCapturedOptionalRoster(t *testing.T) {
	for _, malformed := range []any{nil, true, []any{"web-researcher", 7}} {
		mgr, _, blueprintMgr, _ := testManager(t)
		setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
		ctx := context.Background()
		mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
		run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
		testutil.FailErr(t, "start review", err)
		_, err = mgr.StampRunVars(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			vars["review_if_spawnable"] = map[string]any{"challenge": malformed}
			return vars, true, nil
		})
		testutil.FailErr(t, "remove valid roster", err)
		mgr.ReviewSpawnFilter = func(context.Context, string, string, []string) []string {
			t.Fatal("verdict recomputed phase-entry roster")
			return nil
		}
		mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
			return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
		}
		out, err := mgr.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
		if err == nil || out.Terminal {
			t.Fatalf("roster %v: outcome=%+v, err=%v", malformed, out, err)
		}
		current, err := mgr.Get(ctx, run.ID)
		testutil.FailErr(t, "read held review", err)
		if current.CurrentPhase != "challenge" {
			t.Fatalf("missing roster advanced to %q", current.CurrentPhase)
		}
	}
}

func TestReviewRepairBlocksOnceAndResumeRetainsWork(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 3; i++ {
		msg := api.Message{ID: fmt.Sprintf("result-%d", i), WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", Outcome: api.ToolResultOutcomeRejected, AssistantMessageID: fmt.Sprintf("response-%d", i), ToolCallID: fmt.Sprintf("call-%d", i), Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "verdict.coverage"}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
		testutil.FailErr(t, "record repair", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
		testutil.FailErr(t, "replay repair", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
	}
	got, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusPaused || got.PauseReason != ReviewBlockedReason {
		t.Fatalf("run did not pause: %+v", got)
	}
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read vars", err)
	repair, err := CurrentReviewRepair(vars, "judge")
	testutil.FailErr(t, "read repair", err)
	if repair == nil || len(repair.Responses) != 3 || repair.State != "blocked" {
		t.Fatalf("repair: %+v", repair)
	}
	if mgr.ActiveReviewVerdictPending(t.Context(), run.SessionID) {
		t.Fatal("blocked review still demands a verdict")
	}
	if gateSatisfiedInVars(vars, "evidence_passed:rl_key") {
		t.Fatal("block passed evidence gate")
	}
	resumed, err := mgr.Resume(t.Context(), run.ID)
	testutil.FailErr(t, "resume", err)
	if resumed.CurrentPhase != "judge" || resumed.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("resume changed work: %+v", resumed)
	}
}

func TestReviewContractBlocksWithoutModelAttempts(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	testutil.FailErr(t, "block impossible contract", ReviewRepairs{mgr}.blockContract(t.Context(), run.ID, fmt.Errorf("host id rejected by schema")))
	got, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read blocked run", err)
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read snapshot", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode snapshot", err)
	if got.Status != api.WorkflowRunStatusPaused || repair == nil || repair.Snapshot == nil || len(repair.Responses) != 0 {
		t.Fatalf("contract not blocked atomically: run=%+v repair=%+v", got, repair)
	}
	if gateSatisfiedInVars(vars, "evidence_passed:rl_key") {
		t.Fatal("contract failure stamped a successful verdict")
	}
}

func TestReviewRepairFingerprintUsesStructuredDefects(t *testing.T) {
	feedback := func(path string, reason string) []api.ToolFeedback {
		return []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": path, "reason": reason}}}
	}
	first := reviewIssueFingerprint(feedback("verdict.coverage", "wording one"))
	if first != reviewIssueFingerprint(feedback("verdict.coverage", "wording two")) {
		t.Fatal("diagnostic prose changes repair identity")
	}
	if first == reviewIssueFingerprint(feedback("verdict.claims", "wording one")) {
		t.Fatal("distinct defects share repair identity")
	}
}

func TestReviewRepairFingerprintOmitsUnreadableRows(t *testing.T) {
	typed := map[string]any{"field": "verdict.coverage"}
	want := reviewDiagnosticIdentity("TOOL_ARGS_INVALID", typed)
	for _, unreadable := range []any{math.NaN(), "not rows"} {
		details := map[string]any{"field": "verdict.coverage", "issues": unreadable, "repairs": unreadable}
		if got := reviewDiagnosticIdentity("TOOL_ARGS_INVALID", details); got != want {
			t.Fatalf("unreadable rows %v changed identity: %q want %q", unreadable, got, want)
		}
	}
}

func TestReviewRepairRecoveryCountsResponsesOnce(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 3; i++ {
		msg := api.Message{ID: fmt.Sprintf("durable-result-%d", i), Role: api.MessageRoleTool, WorkflowRunID: run.ID, CreatedAt: time.Now().UTC(), ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: fmt.Sprintf("call-%d", i), AssistantMessageID: fmt.Sprintf("response-%d", i), Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"workflow_phase": run.CurrentPhase, "field": "verdict.coverage"}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
	}
	testutil.FailErr(t, "recover response accounting", ReviewRepairs{mgr}.Recover(t.Context()))
	testutil.FailErr(t, "repeat recovery", ReviewRepairs{mgr}.Recover(t.Context()))
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read recovered episode", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode recovered episode", err)
	if repair == nil || len(repair.Responses) != 3 || repair.State != "blocked" {
		t.Fatalf("recovery episode: %+v", repair)
	}
}

func TestReviewRepairIgnoresResultsFromPriorPhase(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	msg := api.Message{ID: "old-result", WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "old-call", Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"workflow_phase": "previous_phase"}}}}}
	testutil.FailErr(t, "ignore old result", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read current episode", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode episode", err)
	if repair != nil {
		t.Fatal("prior phase refusal affected current repair budget")
	}
}

func TestAcceptedVerdictResolvesResumedBlockedEpisode(t *testing.T) {
	vars := map[string]any{reviewRepairsKey: []ReviewRepair{{ID: "episode", Phase: "claims", State: "blocked", Snapshot: &ReviewSnapshot{Unavailable: []string{"worker ledger"}}}}}
	updated, err := resolveReviewRepair(vars, "claims")
	testutil.FailErr(t, "resolve resumed episode", err)
	repair, err := CurrentReviewRepair(updated, "claims")
	testutil.FailErr(t, "read resolved episode", err)
	if repair == nil || repair.State != "resolved" || repair.Snapshot == nil {
		t.Fatalf("resolved repair lost historical snapshot: %+v", repair)
	}
}

func TestReviewRepairCountsOneResponseAndRetainsLatestCandidate(t *testing.T) {
	episode := ReviewRepair{}
	if !episode.observeResponse("r1", "first", "a") || !episode.observeResponse("r1", "second", "b") {
		t.Fatal("new results were not retained")
	}
	if len(episode.Responses) != 1 || episode.Repeated != 1 || episode.Fingerprint != "b" {
		t.Fatalf("same response counted twice: %+v", episode)
	}
	if episode.observeResponse("r1", "first", "a") || episode.Fingerprint != "b" {
		t.Fatal("replay replaced the newer diagnostic")
	}
	episode.observeResponse("r2", "third", "b")
	if episode.Repeated != 2 {
		t.Fatal("repetition did not follow the prior response's final defect")
	}
}

func TestReviewRepairTotalBudgetBoundsChangingDefects(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 8; i++ {
		msg := api.Message{ID: fmt.Sprintf("result-%d", i), WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: fmt.Sprintf("call-%d", i), AssistantMessageID: fmt.Sprintf("response-%d", i), Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": fmt.Sprintf("verdict.claims[%d]", i)}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
		testutil.FailErr(t, "record changing defect", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
		got, err := mgr.Get(t.Context(), run.ID)
		testutil.FailErr(t, "read repair state", err)
		if i < 7 && got.Status != api.WorkflowRunStatusRunning {
			t.Fatalf("repair stopped early at %d", i+1)
		}
		if i == 7 && (got.Status != api.WorkflowRunStatusPaused || got.PauseReason != ReviewBlockedReason) {
			t.Fatal("changing defects exceeded the response allowance")
		}
	}
}

func TestReviewRepairFingerprintTracksAllRepairsAsASet(t *testing.T) {
	feedback := func(rows ...verdictRepair) []api.ToolFeedback {
		return []api.ToolFeedback{{Code: ReviewLoopVerdictInvalidCode, Details: map[string]any{"repairs": rows}}}
	}
	first := verdictRepair{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "coverage"}}
	second := verdictRepair{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "claims"}}
	before := reviewIssueFingerprint(feedback(first, second))
	if before != reviewIssueFingerprint(feedback(second, first)) {
		t.Fatal("repair ordering changed defect identity")
	}
	if before == reviewIssueFingerprint(feedback(first)) {
		t.Fatal("fixing a secondary repair did not change defect identity")
	}
}

type unavailableReviewLedger struct{ err error }

func (l unavailableReviewLedger) ListReviewLoopEvidence(context.Context, string, string, string) ([]evidence.Record, error) {
	return nil, l.err
}

func TestReviewVerdictsDoesNotTreatUnreadableLedgerAsEmpty(t *testing.T) {
	want := errors.New("ledger unavailable")
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "claims", ReviewLoop: &workflowdef.ReviewLoopDef{}}}}
	_, err := ReviewVerdicts(t.Context(), unavailableReviewLedger{want}, &api.WorkflowRun{ID: "run", SessionID: "session"}, manifest)
	if !errors.Is(err, want) {
		t.Fatalf("ledger failure hidden: %v", err)
	}
}

func TestReviewRepairDoesNotInferSameDefectFromCodeOrProse(t *testing.T) {
	identity := reviewIssueFingerprint([]api.ToolFeedback{{Code: ReviewLoopVerdictInvalidCode, Details: map[string]any{"reason": "invalid claim"}}})
	if identity != "" {
		t.Fatal("unstructured diagnostic treated as a proven repeated defect")
	}
	var episode ReviewRepair
	for i := 0; i < 3; i++ {
		episode.observeResponse(fmt.Sprint(i), fmt.Sprint(i), identity)
	}
	if episode.Repeated != 0 || len(episode.Responses) != 3 {
		t.Fatalf("unstructured errors charged the wrong budget: %+v", episode)
	}
}

func persistReviewResponse(t *testing.T, mgr *RunManager, session string, msg api.Message) {
	t.Helper()
	assistant := api.Message{ID: msg.ToolResult.AssistantMessageID, Role: api.MessageRoleAssistant, CreatedAt: time.Now().UTC(), ToolCalls: []api.ToolCall{{ID: msg.ToolResult.ToolCallID, Name: "submit_verdict"}}}
	msg.CreatedAt = assistant.CreatedAt.Add(time.Millisecond)
	msg.Role = api.MessageRoleTool
	testutil.FailErr(t, "persist review response", mgr.Sessions.AppendMessages(t.Context(), session, assistant, msg))
}

func TestReviewRepairWaitsForWholeResponse(t *testing.T) {
	assistant := api.Message{ID: "response", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "first", Name: "submit_verdict"}, {ID: "second", Name: "submit_verdict"}}}
	first := api.Message{ID: "result-1", ToolResult: &api.ToolResult{Tool: "submit_verdict", AssistantMessageID: "response", ToolCallID: "first"}}
	second := api.Message{ID: "result-2", ToolResult: &api.ToolResult{Tool: "submit_verdict", AssistantMessageID: "response", ToolCallID: "second"}}
	ready, err := finalReviewResponseResult([]api.Message{assistant, first}, first)
	testutil.FailErr(t, "inspect unfinished response", err)
	if ready {
		t.Fatal("first rejection can interrupt the response's later correction")
	}
	history := []api.Message{assistant, first, second}
	ready, err = finalReviewResponseResult(history, first)
	testutil.FailErr(t, "replay earlier result", err)
	if ready {
		t.Fatal("recovery accounted the earlier candidate")
	}
	ready, err = finalReviewResponseResult(history, second)
	testutil.FailErr(t, "inspect settled response", err)
	if !ready {
		t.Fatal("final candidate was not admitted for repair accounting")
	}
}

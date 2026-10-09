package review_test

import (
	"context"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"

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

func startReviewLoopRun(ctx context.Context, t *testing.T, mgr *workflow.RunManager) *api.WorkflowRun {
	t.Helper()
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": reviewLoopTestManifest()})
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
	mgr.Verdicts.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.SetEvidenceProjectDir(func(context.Context, string) (string, error) { return hostDir, nil })

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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlpersist@1.0.0": manifest})
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
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", terminal, cited, nil); err != nil {
		testutil.FailErr(t, "terminal", err)
	}

	recs, err := mgr.Verdicts.ListReviewLoopEvidenceForType(ctx, "sess-1", run.ID, "claims", evidence.GateTypeSurveyClaims)
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

	got, err := mgr.Store.Runs.Get(ctx, run.ID)
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
	mgr.Verdicts.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.SetEvidenceProjectDir(func(context.Context, string) (string, error) { return hostDir, nil })
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": reviewLoopTestManifest()})
	run := startReviewLoopRun(ctx, t, mgr)

	cited := []api.CitationGroundingCitedEvidence{{Path: "a.go", Line: 1, Excerpt: "x"}}
	needs := map[string]string{"verdict": "NEEDS_REVISION", "winner": "undecided"}
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", needs, cited, nil); err != nil {
		testutil.FailErr(t, "needs", err)
	}

	recs, err := mgr.Verdicts.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
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
	mgr.Verdicts.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.SetEvidenceProjectDir(func(context.Context, string) (string, error) { return hostDir, nil })
	run := startReviewLoopRun(ctx, t, mgr)

	// Ungrounded (nil citations) → no record even when schema-valid.
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil); err != nil {
		testutil.FailErr(t, "ungrounded", err)
	}
	recs, err := mgr.Verdicts.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
	testutil.FailErr(t, "ReadAll after ungrounded", err)
	if len(recs) != 0 {
		t.Fatalf("ungrounded records = %d want 0", len(recs))
	}

	// Invalid → no record.
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "MAYBE", "winner": "B"},
		[]api.CitationGroundingCitedEvidence{{Path: "a.go", Line: 1, Excerpt: "x"}}, nil); err != nil {
		testutil.FailErr(t, "invalid", err)
	}
	recs, err = mgr.Verdicts.EvidenceStore.ReadAll(ctx, hostDir, run.ID, "judge", evidence.GateType("rl_key"))
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
	mgr.Verdicts.OnReviewLoopHeld = func(_ context.Context, _ string, decisionRequired bool) {
		heldCalls = append(heldCalls, decisionRequired)
	}

	// Off-enum verdict → schema-invalid → holds, re-prompts (continue), does NOT consume the cap.
	verdict := map[string]string{"verdict": "MAYBE", "winner": "B"}
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", verdict, nil, nil); err != nil {
		testutil.FailErr(t, "RecordReviewLoopVerdict", err)
	}

	got, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge (invalid verdict must hold)", got.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := runstate.ReviewLoopAttempt(vars, "judge"); n != 0 {
		t.Fatalf("attempt = %d want 0 (invalid verdict is not a review round)", n)
	}
	if len(heldCalls) != 1 || heldCalls[0] != false {
		t.Fatalf("held calls = %v want [false] (continue, not decision-required)", heldCalls)
	}
}

func TestRecordReviewLoopVerdictIterationCap(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	run := startReviewLoopRun(ctx, t, mgr) // rltest judge: iteration_cap 2

	var heldCalls []bool
	mgr.Verdicts.OnReviewLoopHeld = func(_ context.Context, _ string, decisionRequired bool) {
		heldCalls = append(heldCalls, decisionRequired)
	}
	needsRevision := map[string]string{"verdict": "NEEDS_REVISION", "winner": "undecided"}

	// An available round keeps review open.
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil); err != nil {
		testutil.FailErr(t, "verdict 1", err)
	}
	// The final round requires a decision.
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil); err != nil {
		testutil.FailErr(t, "verdict 2", err)
	}
	// Further submissions retain the exhausted count and decision requirement.
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", needsRevision, nil, nil)
	testutil.FailErr(t, "verdict 3", err)
	if out.Valid || !out.IterationCapExceeded || out.Attempt != 2 {
		t.Fatalf("outcome 3 = %+v want invalid+IterationCapExceeded at attempt 2", out)
	}

	got, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge (never advances on NEEDS_REVISION)", got.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := runstate.ReviewLoopAttempt(vars, "judge"); n != 2 {
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
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil); err != nil {
		testutil.FailErr(t, "terminal", err)
	}
	got, err = mgr.Store.Runs.Get(ctx, run.ID)
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlagents@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "rlagents", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	out, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil)
	testutil.FailErr(t, "verdict without skeptic", err)
	if out.Valid || out.Terminal || len(out.MissingAgents) != 1 || out.MissingAgents[0] != "skeptic" {
		t.Fatalf("outcome = %+v want missing skeptic", out)
	}
	held, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if held.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge", held.CurrentPhase)
	}

	testutil.FailErr(t, "AppendMessages", mgr.Verdicts.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	workflowTaskQuery1 := func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	mgr.Fanout.WorkerTasks = workflowTaskQuery1
	mgr.Coverage.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery1
	mgr.Verdicts.WorkerTasks = workflowTaskQuery1
	out, err = mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1",
		map[string]string{"verdict": "SELECTED", "winner": "B"}, nil, nil)
	testutil.FailErr(t, "verdict with skeptic", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("outcome = %+v want terminal", out)
	}
	got, err := mgr.Store.Runs.Get(ctx, run.ID)
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
	mgr.Phases.ReviewSpawnFilter = func(_ context.Context, _, _ string, _ []string) []string {
		return nil
	}
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	testutil.FailErr(t, "AppendMessages skeptic", mgr.Verdicts.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	workflowTaskQuery2 := func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	mgr.Fanout.WorkerTasks = workflowTaskQuery2
	mgr.Coverage.WorkerTasks = workflowTaskQuery2
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery2
	mgr.Verdicts.WorkerTasks = workflowTaskQuery2
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict with skeptic only", err)
	if !out.Valid || !out.Terminal || len(out.MissingAgents) != 0 {
		t.Fatalf("outcome = %+v want terminal without researcher", out)
	}
	got, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if got.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", got.CurrentPhase)
	}
}

func TestRecordReviewLoopVerdictIfSpawnableRequiresResearcher(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	testutil.FailErr(t, "AppendMessages skeptic", mgr.Verdicts.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-skeptic",
			AgentType: "skeptic",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	workflowTaskQuery3 := func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	mgr.Fanout.WorkerTasks = workflowTaskQuery3
	mgr.Coverage.WorkerTasks = workflowTaskQuery3
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery3
	mgr.Verdicts.WorkerTasks = workflowTaskQuery3
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict without researcher", err)
	if out.Valid || out.Terminal || len(out.MissingAgents) != 1 || out.MissingAgents[0] != "web-researcher" {
		t.Fatalf("outcome = %+v want missing web-researcher", out)
	}

	testutil.FailErr(t, "AppendMessages researcher", mgr.Verdicts.Sessions.AppendMessages(ctx, "sess-1", api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-research",
			AgentType: "web-researcher",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}))
	workflowTaskQuery4 := func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}, {WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "web-researcher", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	mgr.Fanout.WorkerTasks = workflowTaskQuery4
	mgr.Coverage.WorkerTasks = workflowTaskQuery4
	mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery4
	mgr.Verdicts.WorkerTasks = workflowTaskQuery4
	out, err = mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
	testutil.FailErr(t, "verdict with researcher", err)
	if !out.Valid || !out.Terminal {
		t.Fatalf("outcome = %+v want terminal", out)
	}
	got, err := mgr.Store.Runs.Get(ctx, run.ID)
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
		mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rlspawn@1.0.0": ifSpawnableReviewManifest()})
		run, err := startRun(ctx, mgr, "sess-1", "rlspawn", "1.0.0")
		testutil.FailErr(t, "start review", err)
		_, err = mgr.Phases.Vars.Stamp(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			vars["review_if_spawnable"] = map[string]any{"challenge": malformed}
			return vars, true, nil
		})
		testutil.FailErr(t, "remove valid roster", err)
		mgr.Phases.ReviewSpawnFilter = func(context.Context, string, string, []string) []string {
			t.Fatal("verdict recomputed phase-entry roster")
			return nil
		}
		workflowTaskQuery5 := func(context.Context, string) ([]api.WorkerTask, error) {
			return []api.WorkerTask{{WorkflowRunID: run.ID, WorkflowPhase: "challenge", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
		}
		mgr.Fanout.WorkerTasks = workflowTaskQuery5
		mgr.Coverage.WorkerTasks = workflowTaskQuery5
		mgr.Verdicts.Questions.WorkerTasks = workflowTaskQuery5
		mgr.Verdicts.WorkerTasks = workflowTaskQuery5
		out, err := mgr.Verdicts.RecordReviewLoopVerdict(ctx, "sess-1", map[string]string{"verdict": "CHALLENGED"}, nil, nil)
		if err == nil || out.Terminal {
			t.Fatalf("roster %v: outcome=%+v, err=%v", malformed, out, err)
		}
		current, err := mgr.Store.Runs.Get(ctx, run.ID)
		testutil.FailErr(t, "read held review", err)
		if current.CurrentPhase != "challenge" {
			t.Fatalf("missing roster advanced to %q", current.CurrentPhase)
		}
	}
}

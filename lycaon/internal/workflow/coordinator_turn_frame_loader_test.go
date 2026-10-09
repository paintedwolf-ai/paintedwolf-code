package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type countingSessionWorkflowStore struct {
	workflow.SessionWorkflowStore
	listCalls int
}

func (s *countingSessionWorkflowStore) ListBySession(ctx context.Context, sessionID string) ([]workflow.SessionWorkflowRecord, error) {
	s.listCalls++
	return s.SessionWorkflowStore.ListBySession(ctx, sessionID)
}

func setupCoordinatorTurnFrameLoader(t *testing.T) (*workflow.CoordinatorTurnFrameLoader, *wire.Session, *workflow.RunManager, workflow.SessionWorkflowStore) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ctx-loader.db")

	store := store.NewSQL(sqlDB)
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	sessionWF := workflow.NewSessionWorkflowSQLStore(sqlDB)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	policy, err := workflow.LoadComposePolicy()
	testutil.FailErr(t, "workflow.LoadComposePolicy failed", err)
	composer := &workflow.Composer{
		SessionStore: sessionWF, Registry: reg, Agents: agents, Policy: policy,
	}
	manifest := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	result, err := composer.Compose(context.Background(), workflow.ComposeRequest{
		SessionID: sess.ID, ManifestYAML: []byte(manifest), SessionPosture: sess.Posture,
		CreatedBy: workflow.ComposeActorCoordinator,
	})
	testutil.FailErr(t, "compose workflow manifest", err)
	manifestReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfMgr := workflow.NewManager(workflow.NewSQLStore(sqlDB), store, manifestReg, nil)
	wfMgr.Resolver = workflow.ManifestResolver{SessionStore: sessionWF}
	workflow.WireBlueprintDepsForTest(wfMgr, dir)
	if _, err := wfMgr.StartHuman(context.Background(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "hotfix-session", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	_ = result
	loader := &workflow.CoordinatorTurnFrameLoader{Runs: wfMgr, SessionStore: sessionWF}
	return loader, sess, wfMgr, sessionWF
}

func TestBuildCoordinatorTurnFrameActiveRunAndBrief(t *testing.T) {
	loader, sess, _, _ := setupCoordinatorTurnFrameLoader(t)
	frame, err := loader.BuildCoordinatorTurnFrame(context.Background(), sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	ctx := frame.RunContext
	if ctx.WorkflowID != "hotfix-session" {
		t.Fatalf("workflow_id = %q", ctx.WorkflowID)
	}
	if ctx.CurrentPhase != "research" {
		t.Fatalf("current_phase = %q", ctx.CurrentPhase)
	}
	if !ctx.HasComposeDraft {
		t.Fatal("expected compose draft")
	}
	if ctx.CoordinatorBrief == "" {
		t.Fatal("expected coordinator brief from compose summary")
	}
}

func TestBuildCoordinatorTurnFrameUsesOneWorkflowRevision(t *testing.T) {
	loader, sess, wfMgr, _ := setupCoordinatorTurnFrameLoader(t)
	active, err := wfMgr.GetActive(context.Background(), sess.ID)
	testutil.FailErr(t, "GetActive", err)
	frame, err := loader.BuildCoordinatorTurnFrame(context.Background(), sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	if frame.WorkflowRevision != active.Revision {
		t.Fatalf("frame revision = %d want %d", frame.WorkflowRevision, active.Revision)
	}
	if frame.RunContext.CurrentPhase != active.CurrentPhase {
		t.Fatalf("context phase = %q want %q", frame.RunContext.CurrentPhase, active.CurrentPhase)
	}
	currentRows := 0
	for _, phase := range frame.Runtime.Phases {
		if phase.ID == frame.RunContext.CurrentPhase {
			currentRows++
		}
	}
	if currentRows != 1 || frame.Runtime.PhaseExit == nil {
		t.Fatalf("runtime is not aligned to current phase: rows=%d exit=%#v", currentRows, frame.Runtime.PhaseExit)
	}
}

func TestBuildCoordinatorTurnFrameReadsComposeRecordsOnce(t *testing.T) {
	loader, sess, _, records := setupCoordinatorTurnFrameLoader(t)
	counting := &countingSessionWorkflowStore{SessionWorkflowStore: records}
	loader.SessionStore = counting
	_, err := loader.BuildCoordinatorTurnFrame(context.Background(), sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	if counting.listCalls != 1 {
		t.Fatalf("compose record reads = %d want 1", counting.listCalls)
	}
}

func TestBuildCoordinatorTurnFrameOmitsLastUserAskResponse(t *testing.T) {
	loader, sess, wfMgr, _ := setupCoordinatorTurnFrameLoader(t)
	run, err := wfMgr.GetActive(context.Background(), sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing active run")
	}
	vars, err := wfMgr.Store.GetScaffoldVars(context.Background(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if vars == nil {
		vars = map[string]any{}
	}
	vars["last_user_ask_response"] = map[string]any{
		"phase_id":      "ask-consume-1",
		"response":      "ship it",
		"response_type": "text",
		"resolved_by":   "user",
		"consumed":      false,
	}
	testutil.FailErr(t, "UpdateVars", wfMgr.Store.UpdateVars(context.Background(), run, "", vars))

	frame, err := loader.BuildCoordinatorTurnFrame(context.Background(), sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	enc, _ := json.Marshal(frame.RunContext)
	if strings.Contains(string(enc), "ship it") || strings.Contains(string(enc), "last_user_ask") {
		t.Fatalf("run context must not project last_user_ask_response: %s", enc)
	}
}

func TestBuildCoordinatorTurnFrameTracksLiveSatisfiedGate(t *testing.T) {
	loader, sess, wfMgr, _ := setupCoordinatorTurnFrameLoader(t)
	run, err := wfMgr.GetActive(context.Background(), sess.ID)
	testutil.FailErr(t, "GetActive", err)
	if run == nil {
		t.Fatal("missing active run")
	}
	vars, err := wfMgr.Store.GetScaffoldVars(context.Background(), run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = workflow.SatisfyGateInVars(vars, "research_satisfied")
	testutil.FailErr(t, "UpdateVars", wfMgr.Store.UpdateVars(
		context.Background(), run, "", vars,
	))

	frame, err := loader.BuildCoordinatorTurnFrame(context.Background(), sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	if frame.Runtime.PhaseExit == nil || frame.Runtime.PhaseExit.Kind != "proof" {
		t.Fatalf("phase exit = %+v", frame.Runtime.PhaseExit)
	}
	if len(frame.RunContext.FailedLeaves) != 0 {
		t.Fatalf("satisfied live gate retained stale leaves: %v", frame.RunContext.FailedLeaves)
	}
	foundSatisfied := false
	for _, phase := range frame.Runtime.Phases {
		if phase.ID == frame.RunContext.CurrentPhase && len(phase.Gates) > 0 && phase.Gates[0].Satisfied {
			foundSatisfied = true
		}
	}
	if !foundSatisfied {
		t.Fatal("current phase gate is not satisfied")
	}
}

func TestEffectiveSummaryHelpers(t *testing.T) {
	now := time.Now().UTC()
	records := []workflow.SessionWorkflowRecord{
		{
			WorkflowID: "a", Version: "1.0.0", CreatedAt: now.Add(-time.Hour),
			EffectiveSummary: wire.ComposeEffectiveSummary{CoordinatorBrief: "older"},
		},
		{
			WorkflowID: "b", Version: "1.0.0", CreatedAt: now,
			EffectiveSummary: wire.ComposeEffectiveSummary{
				CoordinatorBrief: "newest",
				Phases:           []wire.ComposePhaseSummary{{ID: "stub"}},
			},
		},
	}
	summary, ok := workflow.LatestEffectiveSummary(records)
	if !ok || summary.CoordinatorBrief != "newest" {
		t.Fatalf("latest = %+v ok=%v", summary, ok)
	}
	byKey, ok := workflow.EffectiveSummaryByKey(records, "a@1.0.0")
	if !ok || byKey.CoordinatorBrief != "older" {
		t.Fatalf("by key = %+v ok=%v", byKey, ok)
	}
}

func TestPlanResearchDepthNoneOmitsResearch(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	plan, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "reg.Get plan", err)
	vars := workflow.ApplyMergedParams(nil, map[string]string{
		"research_depth": "none",
		"auto_approve":   "true",
	})
	vars = workflow.StampDepthParamSkips(vars, plan)
	if !conditions.DotPathTruthy(vars, "phase_skipped.research") {
		t.Fatal("research_depth=none should omit research")
	}
	if conditions.DotPathTruthy(vars, "phase_skipped.review") {
		t.Fatal("research_depth=none alone must not skip transition-only review")
	}
}

type policyReadStore struct {
	workflow.RunStore
	run        *wire.WorkflowRun
	vars       map[string]any
	err        error
	reads      int
	splitReads int
}

func (s *policyReadStore) ActiveStateBySession(context.Context, string) (*wire.WorkflowRun, map[string]any, error) {
	s.reads++
	return s.run, s.vars, s.err
}
func (s *policyReadStore) ActiveBySession(context.Context, string) (*wire.WorkflowRun, error) {
	s.splitReads++
	return &wire.WorkflowRun{ID: "later-run", CurrentPhase: "later"}, nil
}
func (s *policyReadStore) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	s.splitReads++
	return map[string]any{"revision": "later"}, nil
}

func TestPolicySourceUsesOneActiveRevision(t *testing.T) {
	_, sess, runs, _ := setupCoordinatorTurnFrameLoader(t)
	run, _, err := runs.Store.ActiveStateBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "read initial workflow state", err)
	expected, err := workflow.PolicySource(runs)(t.Context(), sess.ID)
	testutil.FailErr(t, "capture expected workflow", err)
	store := &policyReadStore{RunStore: runs.Store, run: run, vars: map[string]any{"revision": "captured"}}
	runs.Store = store
	state, err := workflow.PolicySource(runs)(t.Context(), sess.ID)
	testutil.FailErr(t, "capture policy state", err)
	if store.reads != 1 || store.splitReads != 0 {
		t.Fatalf("state reads=%d split reads=%d", store.reads, store.splitReads)
	}
	if state.RunID != run.ID || state.Phase != run.CurrentPhase || state.WorkflowID != run.WorkflowID || state.RunStatus != run.Status || state.Vars["revision"] != "captured" {
		t.Fatalf("mixed workflow revision: %+v", state)
	}
	if !reflect.DeepEqual(state.AllowedAgents, expected.AllowedAgents) || !reflect.DeepEqual(state.ManifestRules, expected.ManifestRules) || state.ReviewLoopActive != expected.ReviewLoopActive {
		t.Fatalf("policy projection changed revision: %+v", state)
	}
}

func TestPolicySourceDistinguishesAbsentRunAndReadFailure(t *testing.T) {
	store := &policyReadStore{}
	runs := workflow.NewManager(store, nil, nil, nil)
	state, err := workflow.PolicySource(runs)(t.Context(), "session")
	testutil.FailErr(t, "capture absent workflow", err)
	if state.RunID != "" || len(state.AllowedAgents) == 0 || store.reads != 1 || store.splitReads != 0 {
		t.Fatalf("absent workflow facts=%+v reads=%d/%d", state, store.reads, store.splitReads)
	}
	store.err = errors.New("unavailable workflow store")
	_, err = workflow.PolicySource(runs)(t.Context(), "session")
	if !errors.Is(err, store.err) {
		t.Fatalf("store error became absent workflow: %v", err)
	}
}

type failedPolicyBlueprint struct{ err error }

func (g failedPolicyBlueprint) Get(context.Context, string, string) (*wire.Blueprint, error) {
	return nil, g.err
}

func TestPolicySourcePropagatesBlueprintFailure(t *testing.T) {
	_, sess, runs, _ := setupCoordinatorTurnFrameLoader(t)
	run, vars, err := runs.Store.ActiveStateBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "read initial state", err)
	run.BlueprintPath = "plan.md"
	runs.Store = &policyReadStore{RunStore: runs.Store, run: run, vars: vars}
	unavailable := errors.New("blueprint unavailable")
	runs.BlueprintGet = failedPolicyBlueprint{err: unavailable}
	_, err = workflow.PolicySource(runs)(t.Context(), sess.ID)
	if !errors.Is(err, unavailable) {
		t.Fatalf("blueprint read failure lost: %v", err)
	}
}

type missingBlueprintRules struct{ evaluated bool }

func (r *missingBlueprintRules) Evaluate(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
	r.evaluated = eval.BlueprintPath == "plan.md" && eval.PlanContent == "" && eval.ToolArgs["path"] == "plan.md"
	return nil, nil
}
func TestPolicySourceMissingBlueprintRetainsRepairFacts(t *testing.T) {
	_, sess, runs, _ := setupCoordinatorTurnFrameLoader(t)
	run, vars, err := runs.Store.ActiveStateBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "read initial state", err)
	run.BlueprintPath = "plan.md"
	runs.Store = &policyReadStore{RunStore: runs.Store, run: run, vars: vars}
	runs.BlueprintGet = failedPolicyBlueprint{err: blueprint.ErrNotFound}
	source := workflow.PolicySource(runs)
	state, err := source(t.Context(), sess.ID)
	testutil.FailErr(t, "capture missing blueprint", err)
	if state.BlueprintPath != "plan.md" || state.PlanContent != "" {
		t.Fatalf("missing blueprint facts=%+v", state)
	}
	evaluator := &missingBlueprintRules{}
	engine := toolpolicy.NewEngine(toolpolicy.EngineDeps{Workflows: source, Rules: evaluator})
	err = engine.EvaluateInvoke(t.Context(), sess, "write", map[string]any{"path": "plan.md"})
	testutil.FailErr(t, "evaluate missing blueprint repair", err)
	if !evaluator.evaluated {
		t.Fatal("repair skipped ordinary policy evaluation")
	}
}

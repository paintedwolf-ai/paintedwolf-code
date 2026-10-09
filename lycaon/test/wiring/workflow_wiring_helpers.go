package wiring

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

const topologyWaitBudget = 30 * time.Second

func waitTopologyStageComplete(t *testing.T, mgr *workflow.RunManager, runID, stage string) {
	t.Helper()
	testutil.WaitFor(t, topologyWaitBudget, func() bool {
		vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
		if err != nil {
			return false
		}
		stages, _ := vars["topology_stages"].(map[string]any)
		entry, _ := stages[stage].(map[string]any)
		return entry != nil && entry["complete"] == true
	})
}

func waitWorkflowPhase(t *testing.T, ctx context.Context, mgr *workflow.RunManager, runID, phase string) {
	t.Helper()
	got := ""
	if !testutil.WaitForNoFatal(topologyWaitBudget, func() bool {
		run, err := mgr.Store.Runs.Get(ctx, runID)
		if err != nil || run == nil {
			return false
		}
		got = run.CurrentPhase
		return got == phase
	}) {
		t.Fatalf("workflow phase = %q, want %q", got, phase)
	}
}

func startTopologyWorkflowRun(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, workflowID string) *api.WorkflowRun {
	t.Helper()
	run, err := h.WorkflowMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: workflowID, WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman "+workflowID, err)
	return run
}

func fanOutLegsForSession(t *testing.T, h *Harness, sessionID string) []api.Leg {
	t.Helper()
	if h == nil || h.DelegationStore == nil {
		t.Fatal("delegation store required")
	}
	delegationID, ok := h.DelegationStore.DelegationBySessionID(sessionID)
	if !ok {
		t.Fatal("expected topology delegation for session")
	}
	legs, err := h.DelegationStore.ListLegs(context.Background(), delegationID)
	testutil.FailErr(t, "ListLegs", err)
	return legs
}

func assertFanOutWorkers(t *testing.T, legs []api.Leg, wantProfile string, wantCount int) {
	t.Helper()
	if len(legs) != wantCount {
		t.Fatalf("fan_out legs = %d want %d", len(legs), wantCount)
	}
	for _, leg := range legs {
		if leg.AgentType != wantProfile {
			t.Fatalf("leg %q agent = %q want %q", leg.ID, leg.AgentType, wantProfile)
		}
		if leg.Status != api.LegStatusComplete {
			t.Fatalf("leg %q status = %q want complete", leg.ID, leg.Status)
		}
	}
}

// Expected worker profiles and counts come from the run's topology.
func assertFanOutWorkersMatchTopology(t *testing.T, h *Harness, runID string, legs []api.Leg) {
	t.Helper()
	m, err := h.WorkflowMgr.ManifestForRunID(context.Background(), runID)
	testutil.FailErr(t, "manifest for run "+runID, err)
	workflowID := m.ID
	spec, err := orchestration.LoadTopologyFromFile(extpacks.Bundled(config.PlatformFlows.Join("_topologies", m.Topology+".yaml")))
	testutil.FailErr(t, "load topology "+m.Topology, err)
	if spec.FanOut == nil {
		t.Fatalf("workflow %q topology %q is not fan_out", workflowID, m.Topology)
	}
	wantCount := len(spec.FanOut.Subtasks)
	if spec.FanOut.MaxWorkers > 0 && spec.FanOut.MaxWorkers < wantCount {
		wantCount = spec.FanOut.MaxWorkers
	}
	assertFanOutWorkers(t, legs, spec.FanOut.ProfileID, wantCount)
}

func deliverTopologyReport(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, runID string) {
	t.Helper()
	if _, ok := h.SessionMgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok {
		h.SessionMgr.Runner.Coordinator.Kicks().ClearPending(sess.ID)
	}
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "load report phase", err)
	manifest, err := h.WorkflowMgr.ManifestForRunID(ctx, runID)
	testutil.FailErr(t, "load report manifest", err)
	scope := api.CompletionReportScopePhase
	if manifest.ReportEnabled() {
		scope = api.CompletionReportScopeRun
	}
	messageID := uuid.NewString()
	testutil.FailErr(t, "persist grounded completion", h.WorkflowMgr.Policy.Sessions.AppendMessages(ctx, sess.ID, api.Message{
		ID: messageID, Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
		Content: "Completed assessment.", CreatedAt: time.Now().UTC(), WorkflowRunID: runID,
		Visibility: api.MessageVisibilityTranscript, Grounding: &api.CitationGrounding{Traced: true},
		CompletionReport: &api.CompletionReportMeta{Scope: scope, Phase: run.CurrentPhase},
	}))
	testutil.FailErr(t, "MaybeDeliverTopologyReport", h.WorkflowMgr.Reports.MaybeDeliverTopologyReport(ctx, sess.ID, messageID))
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "Get run after report", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q want complete (phase=%q)", run.Status, run.CurrentPhase)
	}
}

func scaffoldTopologyOutput(t *testing.T, mgr *workflow.RunManager, runID, stage string) string {
	t.Helper()
	vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	outputs, _ := vars["topology_outputs"].(map[string]any)
	text, _ := outputs[stage].(string)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("topology_outputs.%s missing", stage)
	}
	return text
}

func startWorkflowRunAt(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, workflowID, wantPhase string) *api.WorkflowRun {
	t.Helper()
	run, err := h.WorkflowMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: workflowID, WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman "+workflowID, err)
	if run.CurrentPhase != wantPhase {
		t.Fatalf("phase = %q want %s", run.CurrentPhase, wantPhase)
	}
	return run
}

func satisfyFanoutPlannedAndAdvance(t *testing.T, h *Harness, ctx context.Context, runID, dir string, legs []runstate.FanoutPlanLeg, wantPhase string) {
	t.Helper()
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run", err)
	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, runID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetGateSatisfied(vars, "fanout_planned", true)
	rawLegs := make([]any, 0, len(legs))
	for _, leg := range legs {
		rawLegs = append(rawLegs, map[string]any{
			"agent_type": leg.AgentType,
			"prompt":     leg.Prompt,
		})
	}
	plans, _ := vars["fanout_plans"].(map[string]any)
	if plans == nil {
		plans = map[string]any{}
	}
	plans[run.CurrentPhase] = map[string]any{"legs": rawLegs}
	vars["fanout_plans"] = plans
	if err := h.WorkflowMgr.Store.UpdateVars(ctx, run, dir, vars); err != nil {
		testutil.FailErr(t, "UpdateVars plan", err)
	}
	run, err = h.WorkflowMgr.Phases.Advance(ctx, runID)
	testutil.FailErr(t, "Advance plan", err)
	if run.CurrentPhase != wantPhase {
		t.Fatalf("phase = %q want %s", run.CurrentPhase, wantPhase)
	}
}

func settleScanObligationAndAdvance(t *testing.T, h *Harness, ctx context.Context, runID, wantPhase string) {
	t.Helper()
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run", err)
	if run.CurrentPhase == wantPhase {
		return
	}
	if run.CurrentPhase != "ingest" {
		t.Fatalf("phase = %q want ingest", run.CurrentPhase)
	}
	store := scan.NewSQLStore(h.DB)
	scans, err := store.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "ListByWorkflowRunID", err)
	if len(scans) == 0 {
		t.Fatal("scan obligation did not enqueue work")
	}
	for i := range scans {
		switch scans[i].Status {
		case api.CodeScanStatusComplete, api.CodeScanStatusFailed, api.CodeScanStatusTimedOut,
			api.CodeScanStatusCanceled, api.CodeScanStatusSuperseded:
			continue
		case api.CodeScanStatusPending:
			if _, err := store.FinalizePendingFailure(ctx, scans[i].ID, "SCAN_WIRING_SETTLE", "wiring obligation settle"); err != nil {
				testutil.FailErr(t, "FinalizePendingFailure "+scans[i].ID, err)
			}
			continue
		case api.CodeScanStatusRunning:
		}
		if _, err := store.FinalizeFailure(ctx, &scans[i], api.CodeScanStatusFailed, "SCAN_WIRING_SETTLE", "wiring obligation settle"); err != nil {
			testutil.FailErr(t, "FinalizeFailure "+scans[i].ID, err)
		}
	}
	testutil.FailErr(t, "RecordObligationTerminal", h.WorkflowMgr.Obligations.RecordObligationTerminal(ctx, runID, scan.WorkflowObligationKind))
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run after obligation", err)
	if run.CurrentPhase != wantPhase {
		t.Fatalf("phase = %q want %s", run.CurrentPhase, wantPhase)
	}
}

// appendSucceededReviewAgent records a reviewer leg and its child-session evidence.
func appendSucceededReviewAgent(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, agent, workID string) string {
	t.Helper()
	task := api.WorkerTask{
		ParentSessionID: sess.ID, AgentType: agent, Prompt: "review " + agent, Brief: "review " + agent,
		Status: api.WorkerStatusPending, SpawnReason: api.SpawnReasonHumanRequest, Scope: &api.TaskScope{Mode: "read"},
	}
	testutil.FailErr(t, "bind reviewer "+agent, h.WorkflowMgr.Fanout.BindWorkflowTask(ctx, tools.ToolContext{SessionID: sess.ID}, workID, &task))
	testutil.FailErr(t, "enqueue defaults "+agent, worker.ApplyEnqueueDefaults(&task,
		project.ProjectScope{ProjectID: sess.ProjectID, WorkspacePath: sess.WorkspacePath}, worker.DefaultWorkersConfig()))
	workerID, err := h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue "+agent, err)
	child, err := h.Store.CreateChild(ctx, sess, api.SpawnChildRequest{AgentType: agent, Prompt: "review " + agent})
	testutil.FailErr(t, "CreateChild "+agent, err)
	testutil.FailErr(t, "link "+agent, h.WorkerQueue.SetChildSessionID(ctx, workerID, child.ID))
	completeQueuedFixtureWork(t, h, ctx, sess.ProjectID, workerID)
	testutil.FailErr(t, "AppendMessages "+agent, h.Store.AppendMessages(ctx, sess.ID, api.Message{
		Role: api.MessageRoleTool,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:       workerID,
			ChildSessionID: child.ID,
			AgentType:      agent,
			Status:         api.WorkerSummaryStatusComplete,
		},
	}))
	testutil.FailErr(t, "UpsertEvidenceRecord "+agent, h.Store.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "read#1",
		Kind:   "read", Shape: evidence.ShapeFileRegion, SourceTool: "read", Fidelity: evidence.FidelityStructured,
		Path: "review.txt", LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		Body: []string{reviewerEvidenceLine(agent)},
	}))
	return child.ID
}

func reviewerEvidenceLine(agent string) string {
	return "reviewer evidence from " + agent
}

// reviewerCitation cites the appended reviewer's observed line by its leg handle.
func reviewerCitation(childID, agent string) api.CitationGroundingCitedEvidence {
	return api.CitationGroundingCitedEvidence{
		Handle:  childID + ":read#1",
		Excerpt: reviewerEvidenceLine(agent),
	}
}

func satisfyWorkerCycleAndAdvance(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, runID, dir string) {
	t.Helper()
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run", err)
	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, runID)
	testutil.FailErr(t, "GetScaffoldVars execute", err)
	vars = workflow.SetWorkerCycleEvalVars(vars, "job-stub", "complete")
	vars["fanout_settled"] = true
	vars["topology_outputs"] = map[string]any{
		orchestration.TopologyBindStageFanOut: "wiring stub fanout evidence",
	}
	vars["topology_stages"] = map[string]any{
		orchestration.TopologyBindStageFanOut: map[string]any{"complete": true},
	}
	if err := h.WorkflowMgr.Store.UpdateVars(ctx, run, dir, vars); err != nil {
		testutil.FailErr(t, "UpdateVars execute", err)
	}
	if _, err := h.WorkflowMgr.Phases.TryAutoAdvance(ctx, runID); err != nil {
		testutil.FailErr(t, "TryAutoAdvance execute", err)
	}
}

// completeQueuedFixtureWork settles earlier stubbed legs before the target reviewer.
func completeQueuedFixtureWork(t *testing.T, h *Harness, ctx context.Context, projectID, target string) {
	t.Helper()
	for {
		claimed, err := h.WorkerQueue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: projectID, ClaimedBy: "review-fixture", ExecutionTarget: api.ExecutionTargetLocal})
		testutil.FailErr(t, "claim fixture work", err)
		if claimed == nil {
			t.Fatalf("fixture task %s is not claimable", target)
		}
		report := &api.WorkerCompletionReport{LegStatus: "complete"}
		if claimed.WorkflowRunID != "" {
			run, err := h.WorkflowMgr.Store.Runs.Get(ctx, claimed.WorkflowRunID)
			testutil.FailErr(t, "load fixture review run", err)
			manifest, err := h.WorkflowMgr.ManifestForRunID(ctx, run.ID)
			testutil.FailErr(t, "load fixture review manifest", err)
			assignment, err := h.WorkflowMgr.Coverage.CoverageAssignment(ctx, run, manifest, claimed.AgentType)
			testutil.FailErr(t, "load fixture coverage assignment", err)
			if assignment != nil {
				report.CoverageReview = coverageReviewFixture(assignment.Facts)
			}
		}
		won, err := h.WorkerQueue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", CompletionReport: report})
		testutil.FailErr(t, "complete fixture work", err)
		if !won {
			t.Fatal("fixture claim lost")
		}
		testutil.FailErr(t, "deliver fixture outcome", h.WorkerQueue.MarkOutcomeDelivered(ctx, claimed.ID))
		if claimed.ID == target {
			return
		}
	}
}

func coverageReviewFixture(facts reviewcoverage.Facts) *api.CoverageReview {
	review := api.CoverageReview{Revision: facts.Revision}
	for _, rows := range [][]reviewcoverage.Fact{facts.Obligations, facts.Gaps} {
		for _, fact := range rows {
			// This wiring fixture settles scanners as failed and stubs worker execution.
			disposition := reviewcoverage.EssentialOpen
			assessment := api.CoverageAssessment{ID: fact.ID, Disposition: disposition,
				Reason:        "The fixture does not execute the required scans or survey workers.",
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "survey#1"}}}
			for _, obligation := range facts.Obligations {
				assessment.Obligations = append(assessment.Obligations, obligation.ID)
			}
			review.Assessments = append(review.Assessments, assessment)
		}
	}

	return &review
}

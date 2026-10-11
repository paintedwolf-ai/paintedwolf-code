package wiring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecuritySurveyCompletesWithFullCoverageWithoutRepeatingWork(t *testing.T) {
	var h *Harness
	var mu sync.Mutex
	calls := map[string]int{}
	reportBody := ""
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{Fallback: func(ctx context.Context, _ llm.Snapshot) modelcall.Completion {
		jobID := workercontext.Job(ctx)
		mu.Lock()
		calls[jobID]++
		turn := calls[jobID]
		body := reportBody
		mu.Unlock()
		if jobID == "" {
			return modelcall.Completion{Content: body}
		}
		if turn == 1 {
			return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: uuid.NewString(), Name: "read", Args: map[string]any{"path": "audit.go"}}}}
		}
		task, ok := h.Delegations.Queue.Get(jobID)
		if !ok {
			t.Errorf("worker %s missing", jobID)
			return modelcall.Completion{}
		}
		args := map[string]any{"leg_status": "complete", "brief": "Reviewed the assigned boundary.", "objectives_met": []any{"Account for the assigned boundary"}, "findings": []any{map[string]any{"path": "audit.go", "line": 1, "excerpt": "package audit"}}}
		assignment, err := h.Workflows.Manager.Assignments.TaskCoverageAssignment(ctx, task)
		if err != nil {
			t.Errorf("review assignment: %v", err)
			return modelcall.Completion{}
		}
		if assignment != nil && assignment.CoverageRequired {
			args["coverage_review"] = completeCoverageReview(assignment.Subject.Facts, api.CitationGroundingCitedEvidence{Path: "audit.go", Line: 1, Excerpt: "package audit"})
		}
		return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: uuid.NewString(), Name: "complete_leg", Args: args}}}
	}})
	h = BuildForTest(t, WithLLMClient(mock), WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "complete-survey")
	testutil.FailErr(t, "write survey input", os.WriteFile(filepath.Join(dir, "audit.go"), []byte("package audit\n"), 0600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create survey", err)
	run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "2.0.0", Request: "Review each trust boundary."})
	testutil.FailErr(t, "start survey", err)
	scans := scan.NewSQLStore(h.DB)
	inventory, err := scans.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "read scan obligations", err)
	if len(inventory) == 0 {
		t.Fatal("scan inventory missing")
	}
	for range inventory {
		claimed, err := scans.ClaimNext(ctx)
		testutil.FailErr(t, "claim scan", err)
		won, err := scans.MarkComplete(ctx, claimed, &scanoutput.Result{ScannedPaths: []string{"audit.go"}, Categories: claimed.Categories})
		testutil.FailErr(t, "complete scan", err)
		if !won {
			t.Fatal("scan claim lost")
		}
	}
	testutil.FailErr(t, "settle scanner obligation", h.Workflows.Manager.Obligations.RecordObligationTerminal(ctx, run.ID, scan.WorkflowObligationKind))
	waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "plan")
	call := wiringToolContext(sess.ID, dir)
	call.Identity.ProjectID, call.Identity.Agent = sess.ProjectID, "coordinator"
	testutil.FailErr(t, "resolve source root", h.DB.QueryRowContext(ctx, "SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1", sess.ProjectID).Scan(&call.Source.ActiveRootID))
	call.Source.Roots[0].ID = call.Source.ActiveRootID
	invoke := func(name string, args map[string]any) string {
		t.Helper()
		call.Identity.ToolCallID = uuid.NewString()
		out, err := h.ToolRegistry.Run(ctx, name, args, call)
		testutil.FailErr(t, "invoke "+name, err)
		return out
	}
	var legs []any
	for i := 1; i <= 5; i++ {
		legs = append(legs, map[string]any{"agent_type": "security-reviewer", "subject": fmt.Sprintf("Boundary %d", i), "prompt": "Review the assigned boundary.", "done_when": []any{"Account for the assigned boundary"}})
	}
	invoke("fanout_plan", map[string]any{"legs": legs, "threat_model": "Local application reading untrusted project content."})
	_, err = h.Workflows.Manager.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "enter planned execution", err)
	for i := 1; i <= 5; i++ {
		invoke("task", map[string]any{"workflow_work_id": fmt.Sprintf("leg-%d", i), "prompt": "Review the assigned boundary."})
	}
	testutil.FailErr(t, "execute survey workers", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
	waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "claims")
	surveyTasks, err := h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "read survey workers", err)
	if len(surveyTasks) != 5 {
		t.Fatalf("survey workers=%d", len(surveyTasks))
	}
	surveyCitation := api.CitationGroundingCitedEvidence{Handle: surveyTasks[0].ChildSessionID + ":read#1", Excerpt: "package audit"}
	coverage := func() map[string]any {
		current, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
		testutil.FailErr(t, "read coverage phase", err)
		manifest, err := h.Workflows.Manager.Resolver.ForRun(ctx, current)
		testutil.FailErr(t, "read coverage manifest", err)
		facts, err := h.Workflows.Manager.Coverage.CoverageFacts(ctx, current, manifest)
		testutil.FailErr(t, "read coverage facts", err)
		if len(facts.Gaps) != 0 {
			t.Fatalf("normal run has coverage gaps: %+v", facts.Gaps)
		}
		raw, err := json.Marshal(completeCoverageReview(facts, surveyCitation))
		testutil.FailErr(t, "encode coverage review", err)
		var value map[string]any
		testutil.FailErr(t, "decode coverage arguments", json.Unmarshal(raw, &value))
		return value
	}
	submit := func(verdict map[string]any, cited []any) {
		t.Helper()
		out := invoke("submit_verdict", map[string]any{"verdict": verdict, "cited_evidence": cited})
		var result workflowreview.SubmitVerdictToolResult
		testutil.FailErr(t, "decode accepted verdict", json.Unmarshal([]byte(out), &result))
		if !result.OK || !result.Terminal {
			t.Fatalf("verdict result=%+v", result)
		}
	}
	submit(map[string]any{"verdict": "CLAIMED", "threat_model": "Local application reading untrusted project content.", "claims": []any{}, "set_asides": []any{}, "coverage": coverage()}, nil)
	waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "challenge")
	for _, agent := range []string{"skeptic", "web-researcher"} {
		invoke("task", map[string]any{"workflow_work_id": "review/" + agent, "agent_type": agent, "prompt": "Independently review the assigned coverage and source evidence.", "scope": map[string]any{"mode": "read"}})
	}
	testutil.FailErr(t, "execute independent review", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, ""))
	tasks, err := h.Delegations.Queue.ListByWorkflowRunID(ctx, run.ID)
	testutil.FailErr(t, "read completed work", err)
	var citations []any
	for _, task := range tasks {
		if task.WorkflowPhase == "challenge" {
			citations = append(citations, map[string]any{"handle": task.ChildSessionID + ":read#1", "excerpt": "package audit"})
		}
	}
	submit(map[string]any{"verdict": "CHALLENGED", "challenges": []any{}, "set_asides": []any{}, "coverage": coverage()}, citations)
	waitWorkflowPhase(t, ctx, h.Workflows.Manager, run.ID, "report")
	manifest, err := h.Workflows.Manager.Resolver.ForRunID(ctx, run.ID)
	testutil.FailErr(t, "load report brief", err)
	levels := manifest.ReportBrief().LevelLabels()
	raw, err := json.Marshal(map[string]any{"synthesis": "Reviewed all five assigned boundaries and independently checked the coverage. No security issue was found in the fixture.", "cited_evidence": []api.CitationGroundingCitedEvidence{surveyCitation}, "cited_urls": []any{}, "rating": map[string]any{"level": levels[len(levels)-1], "why": "All required work completed without a supported finding."}, "findings": []any{}, "set_asides": []any{}})
	testutil.FailErr(t, "encode final report", err)
	mu.Lock()
	reportBody = string(raw)
	mu.Unlock()
	h.Sessions.Manager.Runner.Coordinator.Kicks().ClearPending(sess.ID)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Deliver the completed security assessment.")
	testutil.FailErr(t, "deliver survey report", err)
	current, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "read finished survey", err)
	if current.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("workflow status=%s phase=%s", current.Status, current.CurrentPhase)
	}
	input, available, err := h.Server.Admin.Workflow.Reports.BuildRunReportInput(ctx, run.ID)
	testutil.FailErr(t, "assemble accepted report", err)
	if !available || input.Completeness() != report.CompletenessComplete {
		t.Fatalf("report available=%t completeness=%s gaps=%+v", available, input.Completeness(), input.Gaps)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/workflow-runs/"+run.ID+"/report", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+h.APIToken)
	response := httptest.NewRecorder()
	h.Server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.HasPrefix(response.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("report response=%d %s", response.Code, response.Body.String())
	}
	if len(tasks) != 7 {
		t.Fatalf("worker count=%d, want five survey workers and two reviewers", len(tasks))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, task := range tasks {
		if calls[task.ID] != 2 {
			t.Fatalf("worker %s needed %d calls, want one read and one completion", task.ID, calls[task.ID])
		}
	}
	if calls[""] > 2 {
		t.Fatalf("report needed %d coordinator calls", calls[""])
	}
}

func completeCoverageReview(facts reviewcoverage.Facts, citation api.CitationGroundingCitedEvidence) *api.CoverageReview {
	review := &api.CoverageReview{Revision: facts.Revision}
	for _, fact := range facts.Obligations {
		review.Assessments = append(review.Assessments, api.CoverageAssessment{ID: fact.ID, Disposition: reviewcoverage.Satisfied, Reason: "Required source scope and recorded outcomes were independently reviewed.", CitedEvidence: []api.CitationGroundingCitedEvidence{citation}})
	}
	return review
}

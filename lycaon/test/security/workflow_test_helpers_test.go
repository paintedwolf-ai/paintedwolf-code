package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

const planStartBody = `{"workflow_id":"plan","workflow_version":"1.0.0","request":"Plan the fixture change"}`

func buildWorkflowHarnessWithManifest(t *testing.T, manifest workflowdef.Manifest) (*wiring.Harness, wire.Session) {
	t.Helper()
	h := wiring.BuildForTest(t)
	h.RegisterManifest(manifest)
	sess := createSessionHTTP(t, h.Server, t.TempDir())
	return h, sess
}

func startHarnessRun(t *testing.T, h *wiring.Harness, sessionID, workflowID, version string) wire.WorkflowRun {
	t.Helper()
	run, err := h.WorkflowMgr.Starts.StartHuman(context.Background(), sessionID, wire.StartWorkflowRunRequest{
		WorkflowID: workflowID, WorkflowVersion: version,
	})
	if err != nil {
		testutil.FailErr(t, "start workflow", err)
	}
	return *run
}

func seedPlanStub(t *testing.T, blueprintMgr *blueprint.Manager, projectID, blueprintPath string) {
	t.Helper()
	if blueprintMgr == nil || blueprintPath == "" {
		t.Fatal("blueprint manager and blueprint path required")
	}
	bp, err := blueprintMgr.Get(context.Background(), projectID, blueprintPath)
	testutil.FailErr(t, "get blueprint before seed", err)
	if _, err := blueprintMgr.Store.UpdateContent(context.Background(), projectID, blueprintPath, conditions.TestPlanContentWithTasks, blueprint.ContentDigest(bp.Content)); err != nil {
		testutil.FailErr(t, "seed plan stub", err)
	}
}

func completePlanIntakeHTTP(t *testing.T, h *wiring.Harness, runID string) wire.WorkflowRun {
	t.Helper()
	ctx := context.Background()
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	if err != nil {
		testutil.FailErr(t, "get workflow run", err)
	}
	if run.CurrentPhase != "intake" {
		return *run
	}
	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		testutil.FailErr(t, "get scaffold vars", err)
	}
	vars["hitl_consulted:intake"] = true
	projectDir := ""
	if sess, err := h.WorkflowMgr.Policy.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
		projectDir = sess.WorkspacePath
	}
	if err := h.WorkflowMgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
		testutil.FailErr(t, "update scaffold vars", err)
	}
	out, err := h.WorkflowMgr.Phases.Advance(ctx, run.ID)
	if err != nil {
		testutil.FailErr(t, "advance workflow", err)
	}
	return *out
}

func completePlanDepthAtNoneHTTP(t *testing.T, h *wiring.Harness, runID, phaseID string) wire.WorkflowRun {
	t.Helper()
	ctx := context.Background()
	run, err := h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	if err != nil {
		testutil.FailErr(t, "get workflow run", err)
	}
	if run.CurrentPhase != phaseID {
		return *run
	}
	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		testutil.FailErr(t, "get scaffold vars", err)
	}
	vars = runstate.SetHostVar(vars, "params."+phaseID+"_depth", "none")
	vars = runstate.ResolveDepthSkip(vars, phaseID, phaseID+"_depth")
	if phaseID == "review" {
		vars = runstate.SetGateSatisfied(vars, "evidence_passed:plan_review", true)
	}
	projectDir := ""
	if sess, getErr := h.WorkflowMgr.Policy.Sessions.Get(ctx, run.SessionID); getErr == nil && sess != nil {
		projectDir = sess.WorkspacePath
	}
	if err := h.WorkflowMgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
		testutil.FailErr(t, "persist depth parameter", err)
	}
	return advancePlanRunHTTP(t, h.Server, runID)
}

func advancePlanRunHTTP(t *testing.T, srv *api.Server, runID string) wire.WorkflowRun {
	t.Helper()
	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+runID+"/advance", workflowCommandBody(t, srv, runID, nil))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("advance status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "decode advance response", err)
	}
	return run
}

// A synthetic revision lets the request reach the unknown-run lookup.
func unknownRunCommandBody(t *testing.T, runID string) *strings.Reader {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"workflow_run_id": runID, "expected_revision": 1})
	if err != nil {
		testutil.FailErr(t, "encode workflow command", err)
	}
	return strings.NewReader(string(raw))
}

func workflowCommandBody(t *testing.T, srv *api.Server, runID string, fields map[string]any) *strings.Reader {
	t.Helper()
	req := authedRequest(t, http.MethodGet, "/v1/workflow-runs/"+runID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get workflow revision status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "decode workflow revision", err)
	}
	payload := map[string]any{"expected_revision": run.Revision}
	for key, value := range fields {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		testutil.FailErr(t, "encode workflow command", err)
	}
	return strings.NewReader(string(raw))
}

func workflowStartBody(t *testing.T, srv *api.Server, sessionID string, fields map[string]any) *strings.Reader {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sessionID+"/workflow-runs/active", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get active workflow status = %d body = %s", w.Code, w.Body.String())
	}
	if run := decodeActiveWorkflowRun(t, w.Body.Bytes()); run != nil {
		fields["replace_run_id"] = run.ID
		fields["expected_revision"] = run.Revision
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		testutil.FailErr(t, "encode workflow start", err)
	}
	return strings.NewReader(string(raw))
}

func workflowCommandJSON(t *testing.T, base, runID string, fields map[string]any) string {
	t.Helper()
	status, body := journeyGet(t, base, "/v1/workflow-runs/"+runID)
	if status != http.StatusOK {
		t.Fatalf("get workflow revision status = %d body = %s", status, body)
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(body, &run); err != nil {
		testutil.FailErr(t, "decode workflow revision", err)
	}
	payload := map[string]any{"expected_revision": run.Revision}
	for key, value := range fields {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		testutil.FailErr(t, "encode workflow command", err)
	}
	return string(raw)
}

func workflowStartJSON(t *testing.T, base, sessionID string, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	status, body := journeyGet(t, base, "/v1/sessions/"+sessionID+"/workflow-runs/active")
	if status != http.StatusOK {
		t.Fatalf("get active workflow status = %d body = %s", status, body)
	}
	if run := decodeActiveWorkflowRun(t, body); run != nil {
		fields["replace_run_id"] = run.ID
		fields["expected_revision"] = run.Revision
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		testutil.FailErr(t, "encode workflow start", err)
	}
	return string(raw)
}

// workflowExitCall returns the exit path and body for the session's active run.
func workflowExitCall(t *testing.T, base, sessionID, reason string) (string, string) {
	t.Helper()
	status, body := journeyGet(t, base, "/v1/sessions/"+sessionID+"/workflow-runs/active")
	if status != http.StatusOK {
		t.Fatalf("get active workflow status = %d body = %s", status, body)
	}
	run := decodeActiveWorkflowRun(t, body)
	if run == nil {
		t.Fatalf("session %s has no active workflow run", sessionID)
	}
	payload := map[string]any{"expected_revision": run.Revision}
	if reason != "" {
		payload["reason"] = reason
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		testutil.FailErr(t, "encode workflow exit", err)
	}
	return "/v1/workflow-runs/" + run.ID + "/exit", string(raw)
}

func advancePlanToExpandHTTP(t *testing.T, h *wiring.Harness, run wire.WorkflowRun) wire.WorkflowRun {
	t.Helper()
	if run.CurrentPhase == "intake" {
		run = completePlanIntakeHTTP(t, h, run.ID)
	}
	if run.CurrentPhase == "research" {
		run = completePlanDepthAtNoneHTTP(t, h, run.ID, "research")
	}
	return run
}

func advancePlanRunToExecuteHTTP(t *testing.T, h *wiring.Harness, srv *api.Server, blueprintMgr *blueprint.Manager, sess wire.Session, run wire.WorkflowRun) wire.WorkflowRun {
	t.Helper()
	run = advancePlanToExpandHTTP(t, h, run)
	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanRunHTTP(t, srv, run.ID)
	if run.CurrentPhase == "review" {
		run = completePlanDepthAtNoneHTTP(t, h, run.ID, "review")
	}
	if run.CurrentPhase == "approve" {
		synced, err := h.WorkflowMgr.Approvals.SyncHumanApproval(h.OwnerCtx(t, context.Background()), run.ID, sess.WorkspacePath)
		if err != nil {
			testutil.FailErr(t, "sync human approval", err)
		}
		if synced == nil {
			t.Fatal("expected run after human approval sync")
		}
		run = *synced
	}
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	return run
}

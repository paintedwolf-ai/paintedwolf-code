package security

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowJourneyE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL
	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"spec"}`, http.StatusAccepted)

	run := openAPIPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/{id}/workflow-runs", map[string]string{"id": sess.ID},
		planStartBody, http.StatusCreated)
	if run.BlueprintPath == "" {
		t.Fatal("workflow_run response missing blueprint_path")
	}
	if run.Status != wire.WorkflowRunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
	if run.CurrentPhase != "research" {
		t.Fatalf("run current_phase = %q, want research", run.CurrentPhase)
	}
	msgs := listMessagesOpenAPI(t, base, map[string]string{"id": sess.ID}, http.StatusOK)
	startBoundary := findBoundary(t, msgs, run.ID, "started")
	if run.StartMessageID != startBoundary.ID {
		t.Fatalf("start_message_id = %q want catalog start boundary %q for scroll anchor",
			run.StartMessageID, startBoundary.ID)
	}
	if startBoundary.WorkflowBoundary.WorkflowID != "plan" {
		t.Fatalf("start boundary workflow_id = %q, want plan", startBoundary.WorkflowBoundary.WorkflowID)
	}
	if startBoundary.WorkflowBoundary.Phase != "research" {
		t.Fatalf("start boundary phase = %q, want research", startBoundary.WorkflowBoundary.Phase)
	}
	advanced := advancePlanWorkflow(t, h, base, &run)

	assertPlanWriteAllowed(t, h, sess.ID)
	finishPlanWorkflow(t, h, base, sess.ID, run, advanced)
}

func advancePlanWorkflow(t *testing.T, h *wiring.Harness, base string, run *wire.WorkflowRun) wire.WorkflowRun {
	t.Helper()
	*run = journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/"+run.ID+"/advance", workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
	if run.CurrentPhase != "expand" {
		t.Fatalf("current_phase = %q want expand before stub gate test", run.CurrentPhase)
	}

	status, body := journeyPost(t, base, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandJSON(t, base, run.ID, nil))
	if status != http.StatusConflict {
		t.Fatalf("advance-with-empty-plan status = %d, want 409; body = %s", status, body)
	}
	assertPhaseGateError(t, body, "expand", "plan_stub_valid")

	seedPlanStub(t, h.Workflows.Blueprints, run.ProjectID, run.BlueprintPath)
	advanced := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/"+run.ID+"/advance", workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
	if advanced.CurrentPhase != "approve" {
		t.Fatalf("after advance current_phase = %q, want approve", advanced.CurrentPhase)
	}
	return advanced
}

func assertPhaseGateError(t *testing.T, body []byte, phase, failedGate string) {
	t.Helper()
	var gateErr wire.ErrorResponse
	if err := json.Unmarshal(body, &gateErr); err != nil {
		t.Fatalf("decode advance error: %v body=%s", err, body)
	}
	if gateErr.Code != "phase_gate_unmet" || gateErr.Details == nil {
		t.Fatalf("phase gate error = %+v", gateErr)
	}
	if got, _ := gateErr.Details["phase"].(string); got != phase {
		t.Fatalf("details.phase = %v, want %s", gateErr.Details["phase"], phase)
	}
	if failedGate == "" {
		return
	}
	if got, _ := gateErr.Details["failed_gate"].(string); got != failedGate {
		t.Fatalf("details.failed_gate = %v, want %s", gateErr.Details["failed_gate"], failedGate)
	}
	leaves, _ := gateErr.Details["failed_leaves"].([]any)
	if len(leaves) == 0 {
		t.Fatalf("details.failed_leaves missing/empty: %v", gateErr.Details["failed_leaves"])
	}
}

func assertPlanWriteAllowed(t *testing.T, h *wiring.Harness, sessionID string) {
	t.Helper()
	ctx := context.Background()
	loaded, err := h.Store.Get(ctx, sessionID)
	testutil.FailErr(t, "h.Store.Get failed", err)
	planPath := settingsoverlay.Rel("blueprints/plan.md")
	err = h.Sessions.Manager.Coordinator.Guards.Policy().EvaluateInvoke(ctx, loaded, "write", map[string]any{
		"path": planPath, "content": conditions.TestPlanContentWithTasks,
	})
	testutil.FailErr(t, "write invoke policy", err)
}

func finishPlanWorkflow(
	t *testing.T,
	h *wiring.Harness,
	base, sessionID string,
	run, advanced wire.WorkflowRun,
) {
	t.Helper()
	ctx := context.Background()
	paused := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/"+run.ID+"/pause", workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
	if paused.Status != wire.WorkflowRunStatusPaused {
		t.Fatalf("pause status = %q", paused.Status)
	}
	if !paused.UpdatedAt.After(advanced.UpdatedAt) {
		t.Fatalf("pause did not bump updated_at: advanced=%s paused=%s", advanced.UpdatedAt, paused.UpdatedAt)
	}
	fetchedPaused := openAPIGetJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/{id}", map[string]string{"id": run.ID}, http.StatusOK)
	if !fetchedPaused.UpdatedAt.Equal(paused.UpdatedAt) {
		t.Fatalf("pause response updated_at lags persisted value: response=%s persisted=%s",
			paused.UpdatedAt, fetchedPaused.UpdatedAt)
	}

	resumed := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/"+run.ID+"/resume", workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
	if resumed.Status != wire.WorkflowRunStatusRunning {
		t.Fatalf("resume status = %q", resumed.Status)
	}
	if !resumed.UpdatedAt.After(paused.UpdatedAt) {
		t.Fatalf("resume did not bump updated_at: paused=%s resumed=%s", paused.UpdatedAt, resumed.UpdatedAt)
	}

	superseded := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/"+sessionID+"/workflow-runs", workflowStartJSON(t, base, sessionID, map[string]any{"workflow_id": "plan", "workflow_version": "1.0.0"}), http.StatusCreated)
	if superseded.ID == run.ID {
		t.Fatal("expected a new run id after human supersede")
	}
	prior, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get prior run after supersede", err)
	if prior.Status != wire.WorkflowRunStatusCanceled {
		t.Fatalf("prior status = %q want canceled after supersede", prior.Status)
	}

	exitPath, exitBody := workflowExitCall(t, base, sessionID, "e2e_exit")
	exited := journeyPostJSON[wire.WorkflowRun](t, base, exitPath, exitBody, http.StatusOK)
	if exited.ID != superseded.ID {
		t.Fatalf("exit run id = %q want superseding run %q", exited.ID, superseded.ID)
	}
	if exited.Status != wire.WorkflowRunStatusCanceled {
		t.Fatalf("exit status = %q, want canceled", exited.Status)
	}
	if exited.CompletedAt == nil {
		t.Fatal("exit must set completed_at")
	}
	if exited.UpdatedAt.Before(*exited.CompletedAt) {
		t.Fatalf("exit response updated_at %s precedes completed_at %s — caller saw stale value",
			exited.UpdatedAt, *exited.CompletedAt)
	}
	if exited.EndMessageID == "" {
		t.Fatal("exit must wire end_message_id back into the run")
	}

	activeStatus, activeBody := journeyGet(t, base, "/v1/sessions/"+sessionID+"/workflow-runs/active")
	if activeStatus != http.StatusOK {
		t.Fatalf("active after catalog exit status = %d, want 200 (fresh ambient); body=%s", activeStatus, activeBody)
	}
	ambient := decodeActiveWorkflowRun(t, activeBody)
	if ambient == nil {
		t.Fatalf("active after catalog exit = null, want a fresh ambient run; body=%s", activeBody)
	}
	if ambient.WorkflowID != "implement" {
		t.Fatalf("active after exit workflow_id = %q, want implement (ambient)", ambient.WorkflowID)
	}
	if ambient.AttachPolicy != "session_create" {
		t.Fatalf("active after exit attach_policy = %q, want session_create (ambient)", ambient.AttachPolicy)
	}
	if ambient.ID == exited.ID {
		t.Fatal("active after exit must be a fresh ambient run, not the exited catalog run")
	}

	again := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/workflow-runs/"+exited.ID+"/cancel", workflowCommandJSON(t, base, exited.ID, nil), http.StatusOK)
	if !again.UpdatedAt.Equal(exited.UpdatedAt) {
		t.Fatalf("idempotent cancel bumped updated_at: was %s now %s",
			exited.UpdatedAt, again.UpdatedAt)
	}
	if again.CompletedAt == nil || !again.CompletedAt.Equal(*exited.CompletedAt) {
		t.Fatalf("idempotent cancel changed completed_at: was %v now %v",
			exited.CompletedAt, again.CompletedAt)
	}

	finalMsgs := journeyGetJSON[wire.SessionTranscriptPage](t, base,
		"/v1/sessions/"+sessionID+"/messages", http.StatusOK).Messages
	endBoundary := findBoundary(t, finalMsgs, exited.ID, "exited")
	if endBoundary.WorkflowBoundary.Phase == "" {
		t.Fatal("end boundary missing phase")
	}
	if endBoundary.ID != exited.EndMessageID {
		t.Fatalf("end boundary id %s does not match run.end_message_id %s",
			endBoundary.ID, exited.EndMessageID)
	}
}

// findBoundary selects one run boundary event.
func findBoundary(t *testing.T, msgs []wire.Message, runID, event string) wire.Message {
	t.Helper()
	for _, m := range msgs {
		if m.Kind != wire.MessageKindWorkflowBoundary {
			continue
		}
		if m.WorkflowRunID != runID {
			continue
		}
		if m.WorkflowBoundary == nil {
			t.Fatalf("workflow_boundary message %s has no WorkflowBoundary payload", m.ID)
		}
		if m.WorkflowBoundary.Event == event {
			return m
		}
	}
	t.Fatalf("no boundary message found with run=%s event=%s; messages=%+v", runID, event, msgs)
	return wire.Message{}
}

func journeyPost(t *testing.T, base, path, body string) (status int, out []byte) {
	t.Helper()
	body = string(mutationRequestBody(t, http.MethodPost, path, body))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, strings.NewReader(body))
	testutil.FailErr(t, "http.NewRequest failed", err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func journeyPostJSON[T any](t *testing.T, base, path, body string, wantStatus int) T {
	t.Helper()
	status, out := journeyPost(t, base, path, body)
	if status != wantStatus {
		t.Fatalf("POST %s status = %d, want %d; body = %s", path, status, wantStatus, out)
	}
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode POST %s: %v body=%s", path, err, out)
	}
	return v
}

func journeyGet(t *testing.T, base, path string) (status int, out []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func journeyGetJSON[T any](t *testing.T, base, path string, wantStatus int) T {
	t.Helper()
	status, out := journeyGet(t, base, path)
	if status != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body = %s", path, status, wantStatus, out)
	}
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, out)
	}
	return v
}

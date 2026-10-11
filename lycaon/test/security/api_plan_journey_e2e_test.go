package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPlanWorkflowJourneyE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL
	blueprintMgr := h.Workflows.Blueprints

	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"spec"}`, http.StatusAccepted)

	run := openAPIPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/{id}/workflow-runs", map[string]string{"id": sess.ID},
		planStartBody, http.StatusCreated)
	if run.BlueprintPath == "" {
		t.Fatal("plan run missing blueprint_path")
	}
	if run.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q want plan", run.WorkflowID)
	}
	if run.CurrentPhase != "research" {
		t.Fatalf("current_phase = %q want research", run.CurrentPhase)
	}

	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)
	if run.Status != wire.WorkflowRunStatusPausedOnChild {
		t.Fatalf("status = %q want paused_on_child", run.Status)
	}
}

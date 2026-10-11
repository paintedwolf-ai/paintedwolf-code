package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAdvanceUnregisteredCompleteWhenRejected(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "unreg",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "only", CompleteWhen: "totally_unknown_gate_xyz"},
		},
	})
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	srv := h.Server
	run := startHarnessRun(t, h, sess.ID, "unreg", "1.0.0")
	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "phase_gate_unmet") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestAdvancePlanStubGateBlockedWithoutFixture(t *testing.T) {
	projectDir := t.TempDir()
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, projectDir)
	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	run = advancePlanToExpandHTTP(t, h, run)
	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "phase_gate_unmet") {
		t.Fatalf("body = %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "plan_stub_valid") {
		t.Fatalf("expected plan_stub_valid gate reason, body = %s", w.Body.String())
	}
}

func TestAdvancePlanStubGateAllowsAfterFixture(t *testing.T) {
	projectDir := t.TempDir()
	h := wiring.BuildForTest(t)
	srv := h.Server
	blueprintMgr := h.Workflows.Blueprints
	sess := createSessionHTTP(t, srv, projectDir)
	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanToExpandHTTP(t, h, run)
	req = authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("advance status = %d body = %s", w.Code, w.Body.String())
	}
}

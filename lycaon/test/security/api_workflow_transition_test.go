package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func loadChoiceTransitionsManifest(t *testing.T) workflowdef.Manifest {
	t.Helper()
	path := filepath.Join("..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	return workflowdef.FinalizeManifest(m)
}

func TestFireWorkflowTransitionHTTP(t *testing.T) {
	manifest := loadChoiceTransitionsManifest(t)
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	run, err := h.WorkflowMgr.StartHuman(t.Context(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "start choice workflow", err)

	body := `{"expected_revision":` + strconv.FormatInt(run.Revision, 10) + `}`
	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/transitions/critique", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var out wire.WorkflowRun
	testutil.FailErr(t, "unmarshal", json.Unmarshal(w.Body.Bytes(), &out))
	if out.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", out.CurrentPhase)
	}
}

func TestFireWorkflowTransitionHTTPUnknown(t *testing.T) {
	manifest := loadChoiceTransitionsManifest(t)
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	run, err := h.WorkflowMgr.StartHuman(t.Context(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "start choice workflow", err)

	body := `{"expected_revision":` + strconv.FormatInt(run.Revision, 10) + `}`
	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/transitions/nope", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var errBody wire.ErrorResponse
	testutil.FailErr(t, "unmarshal", json.Unmarshal(w.Body.Bytes(), &errBody))
	if errBody.Code != "choice_transition_not_found" {
		t.Fatalf("code = %q", errBody.Code)
	}
}

func TestGetWorkflowRunChoiceTransitionsUI(t *testing.T) {
	manifest := loadChoiceTransitionsManifest(t)
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	run, err := h.WorkflowMgr.StartHuman(t.Context(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "start choice workflow", err)

	req := authedRequest(t, http.MethodGet, "/v1/workflow-runs/"+run.ID, nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var out wire.WorkflowRun
	testutil.FailErr(t, "unmarshal", json.Unmarshal(w.Body.Bytes(), &out))
	if out.UI == nil || len(out.UI.ChoiceTransitions) != 3 {
		t.Fatalf("ui.choice_transitions = %+v want len 3", out.UI)
	}
}

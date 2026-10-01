package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowPersistJourneyE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, base, projectDir)
	// The helper waits for workspace preparation after the accepted response.
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"spec"}`, http.StatusAccepted)

	composeBody := `{"template_id":"hotfix-template","params":{"workflow_id":"persist-journey-1"}}`
	composed := journeyPostJSON[wire.ComposeWorkflowResponse](t, base,
		"/v1/sessions/"+sess.ID+"/workflows/compose-from-template",
		composeBody, http.StatusCreated)
	if composed.Summary.ID != "persist-journey-1" {
		t.Fatalf("compose summary id = %q, want persist-journey-1", composed.Summary.ID)
	}
	if composed.Summary.Trigger != "/plan" {
		t.Fatalf("compose summary trigger = %q, want /plan (inherited from extends=plan@1.0.0)", composed.Summary.Trigger)
	}

	// The inherited trigger collides with the bundled workflow.
	status, body := journeyPost(t, base,
		"/v1/sessions/"+sess.ID+"/workflows/persist-journey-1/persist",
		`{"version":"1.0.0","confirm":true}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("persist-without-trigger status = %d, want 422; body = %s", status, body)
	}
	var errResp workflowValidationFailure
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode 422: %v body=%s", err, body)
	}
	if len(errResp.Details.Errors) == 0 {
		t.Fatalf("422 had no errors: %+v", errResp)
	}
	gotCode := errResp.Details.Errors[0].Code
	if gotCode != "trigger_collision" {
		t.Fatalf("first error code = %q, want trigger_collision; errors=%+v", gotCode, errResp.Details.Errors)
	}

	persistResp := openAPIPostJSON[wire.PersistWorkflowResponse](t, base,
		"/v1/sessions/{id}/workflows/{workflow_id}/persist",
		map[string]string{"id": sess.ID, "workflow_id": "persist-journey-1"},
		`{"version":"1.0.0","confirm":true,"trigger":"/persist-journey-1"}`,
		http.StatusCreated)
	// The overlay loader discovers workflows in named subdirectories.
	if persistResp.Path != settingsoverlay.DirName()+"/workflows/persist-journey-1/workflow.yaml" {
		t.Fatalf("persist path = %q", persistResp.Path)
	}
	if persistResp.Summary.Scope != wire.WorkflowScopeProject {
		t.Fatalf("persist summary scope = %q, want project", persistResp.Summary.Scope)
	}
	if persistResp.Summary.Trigger != "/persist-journey-1" {
		t.Fatalf("persist summary trigger = %q, want /persist-journey-1", persistResp.Summary.Trigger)
	}

	catalog := openAPIGetJSON[wire.WorkflowListResponse](t, base,
		"/v1/workflows?session_id="+sess.ID, nil, http.StatusOK).Workflows
	var found *wire.WorkflowSummary
	for i := range catalog {
		if catalog[i].ID == "persist-journey-1" {
			found = &catalog[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("catalog missing persist-journey-1: %+v", catalog)
	}
	if found.Scope != wire.WorkflowScopeProject {
		t.Fatalf("catalog scope = %q, want project", found.Scope)
	}
	if found.Trigger != "/persist-journey-1" {
		t.Fatalf("catalog trigger = %q: persisted workflow lost its trigger", found.Trigger)
	}

	// Promotion removes the session-scoped draft.
	status2, body2 := journeyPost(t, base,
		"/v1/sessions/"+sess.ID+"/workflows/persist-journey-1/persist",
		`{"version":"1.0.0","confirm":true,"trigger":"/persist-journey-1"}`)
	if status2 != http.StatusNotFound {
		t.Fatalf("re-persist after promote status = %d, want 404; body=%s", status2, body2)
	}

	run := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/"+sess.ID+"/workflow-runs",
		`{"workflow_id":"persist-journey-1","workflow_version":"1.0.0"}`,
		http.StatusCreated)
	if run.WorkflowID != "persist-journey-1" {
		t.Fatalf("run workflow_id = %q", run.WorkflowID)
	}
	if run.Status != wire.WorkflowRunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
}

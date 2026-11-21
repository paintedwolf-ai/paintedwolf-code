package workflowadmin_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Every response carrying a workflow run carries its derived presentation: the
// stored row has none, so a handler that skips enrichment ships a bare run.
func TestWorkflowRunResponsesCarryRunPresentation(t *testing.T) {
	f := newRoutesFixture(t, nil)
	sess := f.createSession(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	run := &wire.WorkflowRun{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: f.project.ID,
		WorkflowID: "implement", WorkflowVersion: "1.0.0",
		Status: wire.WorkflowRunStatusRunning, CurrentPhase: "work", CreatedAt: at, UpdatedAt: at,
	}
	testutil.FailErr(t, "create run", f.runs.State.CreateState(t.Context(), run, f.workDir, nil))
	stored, err := f.runs.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload run", err)
	if stored.UI != nil {
		t.Fatalf("stored run carries presentation %+v; the fixture cannot tell enrichment apart", stored.UI)
	}

	got := decodeOK[wire.WorkflowRun](t, f.do(t, http.MethodGet, "/v1/workflow-runs/"+run.ID, "", ""), http.StatusOK)
	assertRunPresentation(t, "get run", &got)
	active := decodeOK[wire.ActiveWorkflowRunResponse](t, f.do(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", "", ""), http.StatusOK)
	assertRunPresentation(t, "active run", active.Run)
	page := decodeOK[wire.WorkflowRunPage](t, f.do(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs", "", ""), http.StatusOK)
	if len(page.Runs) != 1 {
		t.Fatalf("session runs = %+v, want the one run", page.Runs)
	}
	assertRunPresentation(t, "listed run", &page.Runs[0])
	paused := decodeOK[wire.WorkflowRun](t, f.postJSON(t, "/v1/workflow-runs/"+run.ID+"/pause", fmt.Sprintf(`{"expected_revision":%d}`, stored.Revision)), http.StatusOK)
	assertRunPresentation(t, "paused run", &paused)
}

func assertRunPresentation(t *testing.T, what string, run *wire.WorkflowRun) {
	t.Helper()
	if run == nil || run.UI == nil || run.UI.Definition == nil {
		t.Fatalf("%s carries no presentation: %+v", what, run)
	}
	if run.UI.Definition.ID != "implement" || run.UI.Definition.Version != "1.0.0" || run.UI.CurrentPhaseLabel == "" || run.UI.PhaseTerminal {
		t.Fatalf("%s presentation = %+v, want the implement definition at its working phase", what, run.UI)
	}
}

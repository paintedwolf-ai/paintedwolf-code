package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestResearchDepthNoneSetsPlanCatalogPredicate(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	workflowStore := h.Workflows.Manager.Store
	projectDir := t.TempDir()
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
	run = completePlanIntakeHTTP(t, h, run.ID)
	if run.CurrentPhase != "research" {
		t.Fatalf("current_phase = %q want research before resolving depth", run.CurrentPhase)
	}
	run = completePlanDepthAtNoneHTTP(t, h, run.ID, "research")

	vars, err := workflowStore.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "workflowStore.GetScaffoldVars failed", err)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	matched, err := condReg.Evaluate("phase_skipped:research", conditions.EvalContext{Vars: vars})
	if err != nil || !matched {
		t.Fatalf("phase_skipped:research = %v err=%v vars=%v", matched, err, vars)
	}
}

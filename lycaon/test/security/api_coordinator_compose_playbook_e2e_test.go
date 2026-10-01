package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestCoordinatorComposeTemplateThenStartRun(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	body := `{"template_id":"hotfix-template","params":{"workflow_id":"hotfix-session"}}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose-from-template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("compose status = %d body = %s", w.Code, w.Body.String())
	}
	var composeResp wire.ComposeWorkflowResponse
	if err := json.Unmarshal(w.Body.Bytes(), &composeResp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if strings.TrimSpace(composeResp.EffectiveSummary.CoordinatorBrief) == "" {
		t.Fatal("expected coordinator_brief in compose response")
	}

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/coordinator-context", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("coordinator-context status = %d body = %s", w.Code, w.Body.String())
	}
	var ctx wire.CoordinatorRunContext
	if err := json.Unmarshal(w.Body.Bytes(), &ctx); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !ctx.HasComposeDraft {
		t.Fatal("expected has_compose_draft")
	}
	if !strings.Contains(ctx.CoordinatorBrief, composeResp.EffectiveSummary.CoordinatorBrief) {
		t.Fatalf("context brief = %q", ctx.CoordinatorBrief)
	}

	startBody := `{"workflow_id":"hotfix-session","workflow_version":"1.0.0"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/coordinator-context", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ctx); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if ctx.WorkflowID != "hotfix-session" {
		t.Fatalf("workflow_id = %q", ctx.WorkflowID)
	}
	if strings.TrimSpace(ctx.CurrentPhase) == "" {
		t.Fatal("expected current_phase")
	}
}

func TestCoordinatorDryRunComposeBeforeUpsert(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	body := `{"template_id":"hotfix-template","params":{"workflow_id":"dry-run-session"}}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose-from-template?dry_run=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dry_run compose status = %d body = %s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/coordinator-context", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var ctx wire.CoordinatorRunContext
	if err := json.Unmarshal(w.Body.Bytes(), &ctx); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if ctx.HasComposeDraft {
		t.Fatal("dry_run should not persist compose draft")
	}

	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflows/compose-from-template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert compose status = %d body = %s", w.Code, w.Body.String())
	}

	startBody := `{"workflow_id":"dry-run-session","workflow_version":"1.0.0"}`
	req = authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start after upsert status = %d body = %s", w.Code, w.Body.String())
	}
}

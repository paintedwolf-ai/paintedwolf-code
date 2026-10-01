package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowCatalogIncludesPlan(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	_ = createSessionHTTP(t, srv, t.TempDir())

	req := authedRequest(t, http.MethodGet, "/v1/workflows", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/workflows", nil)
	var listed wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	catalog := listed.Workflows
	foundPlan := false
	for _, item := range catalog {
		if item.ID == "plan" && item.Trigger == "/plan" {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("catalog missing plan: %+v", catalog)
	}
}

func TestPlanSlashReplacesActivePlanRun(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("history status = %d", w.Code)
	}
	var historyPage wire.WorkflowRunPage
	if err := json.Unmarshal(w.Body.Bytes(), &historyPage); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	history := historyPage.Runs
	if len(history) < 1 {
		t.Fatal("expected run history")
	}
	first := waitActiveWorkflowRunHTTP(t, srv, sess.ID, "plan", 10*time.Second)

	acceptPromptHTTP(t, srv, sess.ID, "/plan")
	waitWorkflowRunCountHTTP(t, srv, sess.ID, len(history)+1, 10*time.Second)

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("history after /plan status = %d", w.Code)
	}
	var historyAfterPage wire.WorkflowRunPage
	if err := json.Unmarshal(w.Body.Bytes(), &historyAfterPage); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	historyAfter := historyAfterPage.Runs
	if len(historyAfter) != len(history)+1 {
		t.Fatalf("/plan run count: before=%d after=%d", len(history), len(historyAfter))
	}
	var oldRun, newRun *wire.WorkflowRun
	for i := range historyAfter {
		run := &historyAfter[i]
		if run.ID == first.ID {
			oldRun = run
		} else if run.WorkflowID == "plan" {
			newRun = run
		}
	}
	if oldRun == nil || oldRun.Status != wire.WorkflowRunStatusCanceled {
		t.Fatalf("replaced plan run = %+v, want canceled", oldRun)
	}
	if newRun == nil || newRun.ID == first.ID || newRun.Status != wire.WorkflowRunStatusRunning {
		t.Fatalf("new plan run = %+v, want distinct active plan", newRun)
	}
}

func TestNonSlashPromptAcceptedHTTP(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	exitAmbientRunHTTP(t, srv, sess.ID)

	postPromptAndWaitTranscriptHTTP(t, srv, sess.ID, "/notatrigger please explain", "I understand", 10*time.Second)
}

func TestSlashPlanStartsWorkflow(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	exitAmbientRunHTTP(t, srv, sess.ID)

	// The active run is observable before the mock coordinator turn settles.
	acceptPromptHTTP(t, srv, sess.ID, "/plan")
	waitActiveWorkflowRunHTTP(t, srv, sess.ID, "plan", 10*time.Second)
}

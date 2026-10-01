package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// Den resume path requires an active or list-resolvable workflow run even when the transcript is empty.
func TestSessionEmptyTranscriptHasActiveAmbientRunHTTP(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET active status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}/workflow-runs/active", map[string]string{"id": sess.ID})

	run := decodeActiveWorkflowRun(t, w.Body.Bytes())
	if run == nil || run.WorkflowID != "implement" || run.AttachPolicy != "session_create" {
		t.Fatalf("active run body = %s", w.Body.String())
	}
}

// Transcript messages can reference terminal runs omitted from active-run lists.
func TestSessionTranscriptWorkflowRunIdHTTP(t *testing.T) {
	h := wiring.BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess := createSessionHTTP(t, h.Server, dir)

	run, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "GetActive", err)
	if run == nil {
		t.Fatal("expected ambient run")
	}
	runID := run.ID
	exitAmbientRunHTTP(t, h.Server, sess.ID)

	if err := h.Store.AppendMessages(ctx, sess.ID, api.Message{
		Role:          api.MessageRoleUser,
		Content:       "after complete",
		WorkflowRunID: runID,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}

	getRun := authedRequest(t, http.MethodGet, "/v1/workflow-runs/"+runID, nil)
	wRun := httptest.NewRecorder()
	h.Server.ServeHTTP(wRun, getRun)
	if wRun.Code != http.StatusOK {
		t.Fatalf("GET workflow run status = %d body = %s", wRun.Code, wRun.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, wRun, http.MethodGet, "/v1/workflow-runs/{id}", map[string]string{"id": runID})

	listReq := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/messages", nil)
	wMsgs := httptest.NewRecorder()
	h.Server.ServeHTTP(wMsgs, listReq)
	if wMsgs.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d body = %s", wMsgs.Code, wMsgs.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, wMsgs, http.MethodGet, "/v1/sessions/{id}/messages", map[string]string{"id": sess.ID})
}

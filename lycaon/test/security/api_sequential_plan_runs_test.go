package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSequentialPlanRunsE2E(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop(), wiring.WithRecordingLLM())
	srv := h.Server
	sessionStore := h.Store
	blueprintMgr := h.Workflows.Blueprints
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()

	start := func() wire.WorkflowRun {
		t.Helper()
		req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", workflowStartBody(t, srv, sess.ID, map[string]any{"workflow_id": "plan", "workflow_version": "1.0.0", "request": "Plan the fixture change"}))
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
		return run
	}

	runA := start()
	if runA.BlueprintPath == "" {
		t.Fatal("expected blueprint_path on run")
	}
	seedPlanStub(t, blueprintMgr, runA.ProjectID, runA.BlueprintPath)

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(`{"text":"during plan A"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("prompt during run status = %d body = %s", w.Code, w.Body.String())
	}
	var accepted wire.PromptAcceptedResponse
	testutil.FailErr(t, "decode admitted prompt", json.Unmarshal(w.Body.Bytes(), &accepted))
	if !testutil.WaitForNoFatal(promptIdleBudget, func() bool {
		submission, err := sessionStore.GetPromptSubmission(ctx, accepted.OperationID)
		testutil.FailErr(t, "read admitted prompt", err)
		if !submission.Status.Terminal() {
			return false
		}
		if submission.Status != store.PromptSubmissionComplete {
			t.Fatalf("prompt status = %s: %s", submission.Status, submission.Error)
		}
		return true
	}) {
		submission, readErr := sessionStore.GetPromptSubmission(ctx, accepted.OperationID)
		last := h.Recording.LastRequest()
		t.Logf("last prompt context: %+v", last.Debug)
		for _, message := range last.Messages {
			if message.Role != wire.MessageRoleSystem {
				t.Logf("prompt message: role=%s kind=%s content=%s tools=%+v", message.Role, message.Kind,
					message.Content[:min(len(message.Content), 500)], message.ToolCalls)
			}
		}
		t.Fatalf("plan A prompt did not finish: submission=%+v read_err=%v", submission, readErr)
	}

	exitReq := workflowExitRequest(t, srv, sess.ID, "user_exit")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, exitReq)
	if w.Code != http.StatusOK {
		t.Fatalf("exit status = %d body = %s", w.Code, w.Body.String())
	}

	runB := start()
	if runB.BlueprintPath == "" || runB.BlueprintPath == runA.BlueprintPath {
		t.Fatalf("plan ids A=%q B=%q", runA.BlueprintPath, runB.BlueprintPath)
	}

	msgs, err := sessionStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var runAMsgs, runBMsgs, boundaries int
	planAPromptFound := false
	for _, m := range msgs {
		if m.Role == wire.MessageRoleUser && m.Content == "during plan A" {
			planAPromptFound = true
			if m.WorkflowRunID != runA.ID {
				t.Fatalf("plan A prompt tagged with run %q, want %q", m.WorkflowRunID, runA.ID)
			}
		}
		if m.Kind == wire.MessageKindWorkflowBoundary {
			boundaries++
		}
		switch m.WorkflowRunID {
		case runA.ID:
			runAMsgs++
		case runB.ID:
			runBMsgs++
		}
	}
	if !planAPromptFound {
		t.Fatal("plan A prompt missing from transcript")
	}
	if boundaries < 3 {
		t.Fatalf("expected start+exit+start boundaries, got %d", boundaries)
	}
	if runAMsgs == 0 {
		t.Fatal("expected messages tagged with run A")
	}
	if runBMsgs == 0 {
		t.Fatal("expected messages tagged with run B")
	}
}

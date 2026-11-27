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

func TestHumanPlanStartLoopWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	before, err := h.Store.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "GetMessages before start", err)
	seen := map[string]bool{}
	for _, msg := range before {
		seen[msg.ID] = true
	}

	req := AuthedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(planStartBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	testutil.FailErr(t, "decode started run", json.Unmarshal(w.Body.Bytes(), &run))

	outcome, err := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Outcomes.For(sess.ID).Actions.Await(t.Context(), run.ContinuationID)
	testutil.FailErr(t, "await workflow start", err)
	if outcome.State != wire.ContinuationStateContinued {
		t.Fatalf("start outcome = %+v", outcome)
	}
	msgs, err := h.Store.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read started turn", err)
	for _, msg := range msgs {
		if !seen[msg.ID] && msg.Kind == wire.MessageKindHostLoopWake && msg.WorkflowRunID == run.ID {
			return
		}
	}
	t.Fatal("started turn did not record the workflow wake")
}

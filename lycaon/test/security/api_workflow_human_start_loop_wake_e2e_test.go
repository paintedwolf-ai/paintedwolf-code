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
	wakes := wiring.WatchWakes(t, h.ServeApp, sess.ID)
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	testutil.FailErr(t, "decode started run", json.Unmarshal(w.Body.Bytes(), &run))

	turn := wakes.AwaitCompletedTurn(t, "the started run's wake opens a coordinator turn", "")
	msgs, err := h.Store.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, msg := range msgs {
		if !seen[msg.ID] && msg.Kind == wire.MessageKindHostLoopWake && msg.ID == turn.TurnID && msg.WorkflowRunID == run.ID {
			return
		}
	}
	t.Fatalf("wake turn %s left no loop wake message for run %s\n%s", turn.TurnID, run.ID, wakes.Report())
}

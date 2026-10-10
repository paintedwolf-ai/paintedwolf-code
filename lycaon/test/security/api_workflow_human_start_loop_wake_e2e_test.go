package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestHumanPlanStartLoopWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(planStartBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start run status = %d body = %s", w.Code, w.Body.String())
	}

	testutil.WaitFor(t, 5*time.Second, func() bool {
		h.Sessions.Manager.Runner.Coordinator.CoordinatorLoop().Nudges.DrainPending(t.Context(), sess.ID)
		msgs, err := h.Store.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "GetMessages", err)
		for _, msg := range msgs {
			if msg.Kind == wire.MessageKindHostLoopWake {
				return true
			}
		}
		return false
	})
}

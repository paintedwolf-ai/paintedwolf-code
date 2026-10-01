package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestApprovalHITLSSEPendingAndResolved(t *testing.T) {
	h := newApprovalHITLHarness(t)

	ch, unsub, err := h.hub.Subscribe(h.ctx, events.Subscription{Project: h.sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "h.hub.Subscribe failed", err)
	defer unsub()

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)
	assertCheckpointSSE(t, ch, decisionID, wire.CheckpointStatusPending)

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID,
		strings.NewReader(`{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", w.Code, w.Body.String())
	}

	assertCheckpointSSE(t, ch, decisionID, wire.CheckpointStatusApproved)

	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}
}

func assertCheckpointSSE(t *testing.T, ch <-chan wire.EventEnvelope, checkpointID string, status wire.CheckpointStatus) {
	t.Helper()
	testutil.WaitFor(t, 3*time.Second, func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok {
				return false
			}
			if envelope.Topic != wire.EventTopicCheckpoint {
				return false
			}
			var ev wire.CheckpointEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			return ev.ID == checkpointID && ev.Status == status
		default:
			return false
		}
	})
}

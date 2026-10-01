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
	"github.com/lycaon/lycaon/test/wiring"
)

// Attention events cross project filters. A pending checkpoint persists through publisher coalescing.
func TestAttentionCrossProjectE2E(t *testing.T) {
	h := newApprovalHITLHarness(t)

	// Subscribed to a project the blocked work is NOT in.
	ch, unsub, err := h.hub.Subscribe(h.ctx, events.Subscription{Project: "some-other-project", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)

	row := waitNeedsYouAttentionRow(t, ch, h.sess.ID, 20*time.Second)
	if row.Reason != wire.AttentionReasonCheckpoint {
		t.Fatalf("reason = %q, want checkpoint", row.Reason)
	}
	if row.ProjectID != h.sess.ProjectID {
		t.Fatalf("project_id = %q, want %q", row.ProjectID, h.sess.ProjectID)
	}

	req := authedRequest(t, http.MethodPost,
		"/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID,
		strings.NewReader(`{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", w.Code, w.Body.String())
	}
	if err := <-done; err != nil {
		testutil.FailErr(t, "prompt", err)
	}
	waitAttentionClear(t, ch, h.sess.ID, 20*time.Second)
}

// Transient attention snapshots can precede the pending checkpoint.
func waitNeedsYouAttentionRow(
	t *testing.T,
	ch <-chan wire.EventEnvelope,
	sessionID string,
	deadline time.Duration,
) wire.AttentionRow {
	t.Helper()
	timeout := time.After(deadline)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed before the attention row arrived")
			}
			if env.Topic != wire.EventTopicAttention {
				continue
			}
			var view wire.AttentionView
			if err := json.Unmarshal(env.Data, &view); err != nil {
				continue
			}
			for _, row := range view.Rows {
				if row.SessionID == sessionID && row.Class == wire.AttentionClassNeedsYou {
					return row
				}
			}
		case <-timeout:
			t.Fatalf("no attention row for session %s reached a subscriber of another project", sessionID)
		}
	}
}

func waitAttentionClear(
	t *testing.T,
	ch <-chan wire.EventEnvelope,
	sessionID string,
	deadline time.Duration,
) {
	t.Helper()
	timeout := time.After(deadline)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed before the attention row cleared")
			}
			if env.Topic != wire.EventTopicAttention {
				continue
			}
			var view wire.AttentionView
			if err := json.Unmarshal(env.Data, &view); err != nil {
				continue
			}
			blocked := false
			for _, row := range view.Rows {
				if row.SessionID == sessionID && row.Class == wire.AttentionClassNeedsYou {
					blocked = true
				}
			}
			if !blocked {
				return
			}
		case <-timeout:
			t.Fatalf("session %s stayed on the attention list after its checkpoint was resolved", sessionID)
		}
	}
}

func TestAttentionSnapshotEndpointE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	project := createAPIProjectAtPath(t, base, t.TempDir())
	idle := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	view := openAPIGetJSON[wire.AttentionView](t, base, "/v1/attention", nil, http.StatusOK)
	for _, row := range view.Rows {
		if row.SessionID == idle.ID {
			t.Fatalf("a session that has done nothing must not be asking for attention: %+v", row)
		}
	}
}

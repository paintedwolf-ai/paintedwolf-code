package wiring

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRecordFindingPublishesFindingsSSE(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess1, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "Create session 1", err)

	hub := h.MemoryHub()
	if hub == nil {
		t.Fatal("expected MemoryHub")
	}
	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess1.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	reg := h.ToolRegistry
	_, err = reg.Run(ctx, "record_finding", map[string]any{
		"summary": "peer-visible finding",
		"ref":     "pkg/foo.go",
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    sess1.ID,
		Agent:        "implementer",
		WorkerJobID:  "job-a",
	})
	testutil.FailErr(t, "record_finding", err)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case env := <-ch:
			if env.Topic != wire.EventTopicFindings {
				continue
			}
			var ev wire.FindingsEvent
			testutil.FailErr(t, "decode findings event", json.Unmarshal(env.Data, &ev))
			if ev.Revision == 0 {
				t.Fatalf("revision = 0")
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sess1.ID+"/findings", nil)
			req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
			w := httptest.NewRecorder()
			h.Server.ServeHTTP(w, req)
			var digest wire.FindingsDigest
			testutil.FailErr(t, "decode digest", json.Unmarshal(w.Body.Bytes(), &digest))
			if digest.Revision != ev.Revision {
				t.Fatalf("digest revision = %d event revision = %d", digest.Revision, ev.Revision)
			}
			if len(digest.Findings) != 1 || digest.Findings[0].Summary != "peer-visible finding" {
				t.Fatalf("findings = %+v", digest.Findings)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for findings SSE")
		}
	}
}

package toolusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestLivePromptWaitsForPreparationAndUsesOperationID(t *testing.T) {
	var polls, posted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/session/prompts":
			var req wire.PromptRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode prompt: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, err := uuid.Parse(req.OperationID); err != nil || req.Text != "inspect" {
				t.Errorf("invalid prompt envelope: %+v", req)
			}
			if polls.Load() < 2 {
				t.Error("prompt posted before preparation completed")
			}
			posted.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "submission", MessageID: "submission"})
		case r.URL.Path == "/v1/sessions/session/messages":
			page := wire.SessionTranscriptPage{Messages: []wire.Message{{ID: "submission", Role: wire.MessageRoleUser, Content: "inspect", CreatedAt: time.Now()}, {Role: wire.MessageRoleAssistant, Content: "Inspected."}}}
			switch {
			case r.URL.Query().Get("from") == "oldest":
				page = wire.SessionTranscriptPage{AfterCursor: "next-page"}
			case r.URL.Query().Get("after") == "next-page":
			default:
				t.Errorf("unexpected transcript position: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/v1/sessions/session":
			status := wire.SessionStatusIdle
			if polls.Add(1) == 1 {
				status = wire.SessionStatusPreparing
			}
			_ = json.NewEncoder(w).Encode(wire.Session{ID: "session", Status: status})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := liveClient{base: server.URL, token: "fixture", http: server.Client()}
	testutil.FailErr(t, "post prepared prompt", client.postPromptAndWait(t.Context(), "session", "inspect", 5*time.Second))
	if posted.Load() != 1 {
		t.Fatalf("posted %d prompts, want one", posted.Load())
	}
}

func TestLivePreparationStopsOnFailureOrDeadline(t *testing.T) {
	for _, status := range []wire.SessionStatus{wire.SessionStatusError, wire.SessionStatusPreparing} {
		t.Run(string(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("unprepared session received a prompt")
				}
				_ = json.NewEncoder(w).Encode(wire.Session{Status: status})
			}))
			defer server.Close()
			client := liveClient{base: server.URL, token: "fixture", http: server.Client()}
			err := client.postPromptAndWait(t.Context(), "session", "inspect", 100*time.Millisecond)
			if err == nil {
				t.Fatal("unprepared session accepted")
			}
			if status == wire.SessionStatusPreparing && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("preparation deadline: %v", err)
			}
		})
	}
}

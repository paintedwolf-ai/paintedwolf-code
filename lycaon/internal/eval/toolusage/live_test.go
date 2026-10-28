package toolusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		case strings.HasSuffix(r.URL.Path, "/checkpoints"):
			_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{})
		case strings.HasSuffix(r.URL.Path, "/workflow-runs/active"):
			_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
		case r.URL.Path == "/v1/events":
			serveIdleEvents(w, r)
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

func serveIdleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func TestLiveFailedTurnSettlesWithoutAssistantReply(t *testing.T) {
	posted := make(chan struct{})
	var oldest atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/session/checkpoints":
			_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{})
		case "/v1/sessions/session/workflow-runs/active":
			_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
		case "/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-posted:
			case <-r.Context().Done():
				return
			}
			_, _ = fmt.Fprint(w, `data: {"topic":"session","scope":{"session_id":"session"},"data":{"id":"session","host_error":{"code":"provider_request_rejected"}}}`+"\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/v1/sessions/session/prompts":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{OperationID: "op", MessageID: "user"})
			close(posted)
		case "/v1/sessions/session/messages":
			if r.URL.Query().Get("from") == "oldest" {
				oldest.Add(1)
			}
			_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{Messages: []wire.Message{{ID: "user", Ord: 1, Role: wire.MessageRoleUser}}})
		case "/v1/sessions/session":
			_ = json.NewEncoder(w).Encode(wire.Session{ID: "session", Status: wire.SessionStatusIdle})
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	}))
	defer server.Close()
	client := newLiveClient(server.URL, "fixture")
	start := time.Now()
	err := client.postPromptAndWait(t.Context(), "session", "fail", 5*time.Second)
	var failure *ExecutionFailure
	if !errors.As(err, &failure) || failure.Code != "provider_request_rejected" {
		t.Fatalf("failure = %v, want structured provider code", err)
	}
	if time.Since(start) > time.Second || oldest.Load() > 1 {
		t.Fatalf("failed turn waited %s; oldest reads %d", time.Since(start), oldest.Load())
	}
}

func TestTranscriptPollRetainsPositionAndRefreshesLastRow(t *testing.T) {
	var reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if reads == 1 && r.URL.Query().Get("from") != "oldest" {
			t.Errorf("first position = %s", r.URL.RawQuery)
		}
		if reads > 1 && r.URL.Query().Get("after_ord") != "1" {
			t.Errorf("poll restarted history: %s", r.URL.RawQuery)
		}
		messages := []wire.Message{{ID: "answer", Ord: 2, Role: wire.MessageRoleAssistant, Content: fmt.Sprint(reads)}}
		if reads == 1 {
			messages = append([]wire.Message{{ID: "user", Ord: 1, Role: wire.MessageRoleUser}}, messages...)
		}
		_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{Messages: messages})
	}))
	defer server.Close()
	client := newLiveClient(server.URL, "fixture")
	tail := &transcriptTail{}
	for range 3 {
		messages, err := tail.read(t.Context(), client, "session")
		testutil.FailErr(t, "read incremental transcript", err)
		if len(messages) != 2 || messages[1].Content != fmt.Sprint(reads) {
			t.Fatalf("tail = %+v", messages)
		}
	}
}

func TestSessionEventObservationRejectsLossAndIgnoresOtherSessions(t *testing.T) {
	for _, data := range []string{"", "data: invalid\n", `data: {"topic":"session","scope":{"session_id":"other"},"data":{"id":"other","host_error":{"code":"prompt_failed"}}}` + "\n"} {
		if err := readSessionFailure(strings.NewReader(data), "session"); err == nil {
			t.Fatal("lost event stream accepted")
		}
	}
}

func TestUnattendedLiveWaitReportsBlockedTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checkpoints") {
			_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{{ID: "approval", Status: wire.CheckpointStatusPending}}})
			return
		}
		_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
	}))
	defer server.Close()
	client := newLiveClient(server.URL, "fixture")
	err := client.unhandledInput(t.Context(), "session")
	var failure *ExecutionFailure
	if !errors.As(err, &failure) || failure.Code != "operator_input_required" {
		t.Fatalf("blocked wait = %v", err)
	}
}

package toolusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCompletedSubmissionWithoutAnswerStopsWaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/root/prompts":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "admitted", MessageID: "admitted"})
		case "/v1/sessions/root/messages":
			_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{Messages: []wire.Message{
				{ID: "admitted", Role: wire.MessageRoleUser, Content: "task"},
				{Role: wire.MessageRoleAssistant},
			}})
		default:
			_ = json.NewEncoder(w).Encode(wire.Session{Status: wire.SessionStatusIdle})
		}
	}))
	defer server.Close()
	client := liveClient{base: server.URL, http: server.Client()}
	client.observeCompletion = func(_ context.Context, sessionID, submissionID string) (bool, error) {
		if sessionID != "root" || submissionID != "admitted" {
			t.Fatalf("completion is not bound to admission: %s/%s", sessionID, submissionID)
		}
		return true, nil
	}
	testutil.FailErr(t, "finish without fabricating an answer", client.postPromptAndWait(t.Context(), "root", "task", time.Second))
}

func TestIdleCoordinatorStillServicesChildCheckpoint(t *testing.T) {
	responded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/root/prompts":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "admitted", MessageID: "admitted"})
		case "/v1/sessions/root/checkpoints":
			_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{{SessionID: "child", ID: "approval"}}})
		case "/v1/sessions/root/workflow-runs/active":
			_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
		case "/v1/sessions/root/messages":
			_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{})
		default:
			_ = json.NewEncoder(w).Encode(wire.Session{Status: wire.SessionStatusIdle})
		}
	}))
	defer server.Close()
	client := liveClient{base: server.URL, http: server.Client()}
	client.onHumanInput = func(checkpoints []wire.CheckpointEvent, _ *FeedbackRequest) error {
		if len(checkpoints) != 1 || checkpoints[0].SessionID != "child" {
			t.Fatalf("checkpoints = %+v", checkpoints)
		}
		responded = true
		return nil
	}
	client.observeCompletion = func(context.Context, string, string) (bool, error) { return responded, nil }
	testutil.FailErr(t, "resolve child input while root is idle", client.postPromptAndWait(t.Context(), "root", "task", time.Second))
	if !responded {
		t.Fatal("child checkpoint was ignored")
	}
}

package toolusage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSuiteFeedbackObservation(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattended", true: "supervised"}[wait], func(t *testing.T) {
			result := CaseReport{Status: "running"}
			var states []string
			observe := observeCaseHumanInput(&result, wait, func(update CaseReport) error {
				states = append(states, update.Status)
				return nil
			})
			feedback := &FeedbackRequest{RunID: "run", PendingFeedback: wire.PendingFeedback{
				PhaseID: "question", Prompt: "Which behavior?", IssuedRevision: 6,
				ResponseType: "single_choice", Options: []string{"Keep", "Change"},
			}}
			for range 2 {
				err := observe(nil, feedback)
				var required humanInputRequired
				if errors.As(err, &required) == wait {
					t.Fatalf("wait=%v: input result=%v", wait, err)
				}
			}
			if len(result.FeedbackRequests) != 1 || len(states) != 1 || result.Status != "awaiting_input" {
				t.Fatalf("question lost or duplicated: %+v, %v", result, states)
			}
			testutil.FailErr(t, "resume after answer", observe(nil, nil))
			if result.Status != "running" || len(states) != 2 || result.FeedbackRequests[0].IssuedRevision != 6 {
				t.Fatalf("question history or current status incorrect: %+v", result)
			}
			feedback.IssuedRevision = 7
			_ = observe(nil, feedback)
			if len(result.FeedbackRequests) != 2 || result.FeedbackRequests[0].IssuedRevision != 6 {
				t.Fatalf("reissued phase lost its distinct question: %+v", result)
			}
		})
	}
}

func TestSuiteFeedbackInspectionUsesCurrentHostState(t *testing.T) {
	question := &wire.WorkflowRun{ID: "run", UI: &wire.WorkflowRunUi{
		PendingFeedback: &wire.PendingFeedback{PhaseID: "question", Prompt: "Which behavior?", IssuedRevision: 6},
	}}
	cases := []struct {
		name   string
		status int
		run    *wire.WorkflowRun
	}{
		{name: "active run", status: http.StatusOK, run: question},
		{name: "no active run", status: http.StatusOK},
		{name: "forbidden", status: http.StatusForbidden, run: question},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/owned/workflow-runs/active" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Errorf("unexpected feedback request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{Run: tc.run})
			}))
			defer server.Close()
			client := &liveClient{base: server.URL, token: "fixture", http: server.Client()}
			feedback, err := client.pendingFeedback(t.Context(), "owned")
			if tc.status != http.StatusOK {
				if err == nil {
					t.Fatal("inspection failure must not become absence")
				}
				return
			}
			testutil.FailErr(t, "inspect pending feedback", err)
			if tc.run == nil {
				if feedback != nil {
					t.Fatalf("missing workflow returned a question: %+v", feedback)
				}
			} else if feedback == nil || feedback.RunID != "run" || feedback.PhaseID != "question" || feedback.IssuedRevision != 6 {
				t.Fatalf("lost pending question: %+v", feedback)
			}
		})
	}
}

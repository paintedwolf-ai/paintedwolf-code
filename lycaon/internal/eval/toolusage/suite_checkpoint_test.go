package toolusage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSuiteCheckpointObservation(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattended", true: "supervised"}[wait], func(t *testing.T) {
			result := CaseReport{Status: "running"}
			var states []string
			observe := observeCaseHumanInput(&result, wait, func(update CaseReport) error {
				states = append(states, update.Status)
				return nil
			})
			pending := []wire.CheckpointEvent{{ID: "approval", Status: wire.CheckpointStatusPending}}
			for range 2 {
				err := observe(pending, nil)
				var checkpoint humanInputRequired
				if errors.As(err, &checkpoint) == wait {
					t.Fatalf("wait=%v: checkpoint result=%v", wait, err)
				}
			}
			if len(result.CheckpointRequests) != 1 || len(states) != 1 || result.Status != "awaiting_approval" {
				t.Fatalf("observation lost or duplicated: %+v, %v", result, states)
			}
			testutil.FailErr(t, "resume after external resolution", observe(nil, nil))
			if result.Status != "running" || len(states) != 2 || result.CheckpointRequests[0].Status != wire.CheckpointStatusPending {
				t.Fatalf("historical request or current status changed incorrectly: %+v", result)
			}
		})
	}
}

func TestSuiteCheckpointObserverPreservesReportFailure(t *testing.T) {
	want := errors.New("report unavailable")
	result := CaseReport{Status: "running"}
	observe := observeCaseHumanInput(&result, false, func(CaseReport) error { return want })
	err := observe([]wire.CheckpointEvent{{ID: "approval", Status: wire.CheckpointStatusPending}}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("report failure hidden: %v", err)
	}
}

func TestSuiteCheckpointInspectionNeverResolves(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/owned/checkpoints" || r.URL.Query().Get("status") != "pending" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected checkpoint request: %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{{ID: "approval", Status: wire.CheckpointStatusPending}}})
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, token: "fixture", http: server.Client()}
	checkpoints, err := client.pendingCheckpoints(t.Context(), "owned")
	testutil.FailErr(t, "inspect pending checkpoint", err)
	if len(checkpoints) != 1 || checkpoints[0].ID != "approval" {
		t.Fatalf("lost pending checkpoint: %+v", checkpoints)
	}
}

func TestSuiteRetainsHumanInputBeforeAborting(t *testing.T) {
	for _, tc := range []struct {
		name        string
		question    bool
		abortStatus int
	}{
		{"approval", false, http.StatusOK},
		{"approval abort refused", false, http.StatusForbidden},
		{"question", true, http.StatusOK},
		{"question abort refused", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, capture := t.TempDir(), t.TempDir()
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "input"), []byte("fixture"), 0o600))
			out := filepath.Join(t.TempDir(), "report.json")
			posted, aborted := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/projects":
					_ = json.NewEncoder(w).Encode(wire.Project{ID: "p"})
				case r.URL.Path == "/v1/sessions":
					_ = json.NewEncoder(w).Encode(wire.Session{ID: "s"})
				case strings.HasSuffix(r.URL.Path, "/prompts"):
					stored, err := LoadSuiteReport(out)
					testutil.FailErr(t, "read running report before prompt", err)
					if len(stored.Cases) != 1 || stored.Cases[0].SessionID != "s" {
						t.Error("session not retained before spending")
					}
					posted = true
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "submission", MessageID: "submission"})
				case strings.HasSuffix(r.URL.Path, "/checkpoints"):
					pending := []wire.CheckpointEvent{}
					if !tc.question {
						pending = append(pending, wire.CheckpointEvent{ID: "approval", Status: wire.CheckpointStatusPending})
					}
					_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: pending})
				case strings.HasSuffix(r.URL.Path, "/workflow-runs/active"):
					if !tc.question {
						_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
						return
					}
					_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{Run: &wire.WorkflowRun{ID: "run", UI: &wire.WorkflowRunUi{
						PendingFeedback: &wire.PendingFeedback{PhaseID: "question", Prompt: "Which behavior?"},
					}}})
				case strings.HasSuffix(r.URL.Path, "/abort"):
					stored, err := LoadSuiteReport(out)
					testutil.FailErr(t, "read human input before abort", err)
					retained := len(stored.Cases[0].CheckpointRequests)
					if tc.question {
						retained = len(stored.Cases[0].FeedbackRequests)
					}
					if retained != 1 {
						t.Error("human input not retained before abort")
					}
					aborted = true
					w.WriteHeader(tc.abortStatus)
				case strings.HasSuffix(r.URL.Path, "/messages"):
					_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{})
				default:
					status := wire.SessionStatusIdle
					if posted {
						status = wire.SessionStatusBusy
					}
					_ = json.NewEncoder(w).Encode(wire.Session{ID: "s", Status: status})
				}
			}))
			defer server.Close()
			suite := &Suite{ID: "suite", root: root, Cases: []SuiteCase{{CorpusTask: CorpusTask{ID: "case", Prompt: "inspect"}, Project: "."}}}
			report, err := RunSuite(t.Context(), SuiteOptions{
				LiveOptions: LiveOptions{AllowLive: true, BaseURL: server.URL, Token: "fixture", CaptureDir: capture, Runs: 1, Timeout: time.Second},
				Suite:       suite, ExpectedModel: "fixture", Label: "test", Output: out,
			})
			want := "blocked"
			if tc.abortStatus != http.StatusOK {
				want = "error"
			}
			if !aborted || len(report.Cases) != 1 || report.Cases[0].Status != want || (err != nil) != (tc.abortStatus != http.StatusOK) {
				t.Fatalf("abort=%v, report=%+v, error=%v", aborted, report, err)
			}
		})
	}
}

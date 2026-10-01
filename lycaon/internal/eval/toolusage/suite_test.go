package toolusage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/logview"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func TestPaidEvaluationRequiresOptIn(t *testing.T) {
	if _, err := RunLive(t.Context(), LiveOptions{}); err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("live opt-in: %v", err)
	}
	if _, err := RunSuite(t.Context(), SuiteOptions{}); err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("suite opt-in: %v", err)
	}
}

func TestSuiteLoadAndSelection(t *testing.T) {
	suite, err := LoadSuite("../../../test/fixtures/eval/release-suite.yaml")
	testutil.FailErr(t, "load release suite", err)
	if len(suite.Cases) < 8 || suite.Digest == "" {
		t.Fatalf("incomplete suite: %+v", suite)
	}
	testutil.FailErr(t, "select targeted cases", suite.Select([]string{"relationship-check", "fix-ordering"}))
	if suite.Cases[0].ID != "relationship-check" || len(suite.Cases) != 2 {
		t.Fatal("selection lost requested order")
	}
	if suite.Select([]string{"missing"}) == nil {
		t.Fatal("accepted nonexistent case")
	}
	path := filepath.Join(t.TempDir(), "bad.yaml")
	testutil.FailErr(t, "write malformed suite", os.WriteFile(path, []byte("id: typo\ncasses: []\n"), 0o600))
	if _, err := LoadSuite(path); err == nil {
		t.Fatal("accepted unknown suite fields")
	}
}

func TestSuiteLoadsWithoutAnArbitraryCaseLimit(t *testing.T) {
	original, err := LoadSuite("../../../test/fixtures/eval/coordinator-benchmark.json")
	testutil.FailErr(t, "load fixture bank", err)
	cases := make([]SuiteCase, 64)
	for i := range cases {
		cases[i] = original.Cases[0]
		cases[i].ID = fmt.Sprintf("case-%d", i)
	}
	body, err := yaml.Marshal(Suite{ID: "large-bank", Cases: cases, Unattended: original.Unattended})
	testutil.FailErr(t, "encode large bank", err)
	path := filepath.Join(t.TempDir(), "suite.json")
	testutil.FailErr(t, "write large bank", os.WriteFile(path, body, 0o600))
	suite, err := LoadSuite(path)
	testutil.FailErr(t, "load large bank", err)
	if len(suite.Cases) != len(cases) {
		t.Fatalf("cases: got %d, want %d", len(suite.Cases), len(cases))
	}
}

func TestFixtureCopiesAreIndependentAndRejectSymlinks(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write original fixture", os.WriteFile(filepath.Join(source, "sample"), []byte("original"), 0o600))
	digest, err := snapshotFixture(source, target)
	testutil.FailErr(t, "copy fixture", err)
	if len(digest) != 64 {
		t.Fatalf("fixture digest = %q", digest)
	}
	testutil.FailErr(t, "validate original copy", unchangedFixture(source, target))
	testutil.FailErr(t, "edit copied fixture", os.WriteFile(filepath.Join(target, "sample"), []byte("edited"), 0o600))
	if unchangedFixture(source, target) == nil {
		t.Fatal("missed a changed original")
	}
	body, err := os.ReadFile(filepath.Join(source, "sample"))
	testutil.FailErr(t, "read original fixture", err)
	if string(body) != "original" {
		t.Fatal("copy modified source")
	}
	testutil.FailErr(t, "create forbidden fixture symlink", os.Symlink(filepath.Join(source, "sample"), filepath.Join(source, "link")))
	if _, err := snapshotFixture(source, ""); err == nil {
		t.Fatal("fixture accepted a symlink")
	}
}

func TestIdleWithoutAnswerIsNotCompletion(t *testing.T) {
	now := time.Now()
	messages := []wire.Message{{Role: wire.MessageRoleAssistant, Content: "Old answer"}, {ID: "submission", Role: wire.MessageRoleUser, Content: "new", CreatedAt: now}}
	if promptReady(messages, "submission") {
		t.Fatal("accepted a previous turn's answer")
	}
	messages = append(messages, wire.Message{Role: wire.MessageRoleAssistant, Content: "new answer"})
	if !promptReady(messages, "submission") {
		t.Fatal("missed completed answer")
	}
}

func TestPromptCompletionUsesSubmissionIdentity(t *testing.T) {
	messages := []wire.Message{
		{ID: "previous", Role: wire.MessageRoleUser, Content: "same ask"},
		{Role: wire.MessageRoleAssistant, Content: "Previous answer"},
		{ID: "current", Role: wire.MessageRoleUser, Content: "same ask", CreatedAt: time.Unix(0, 0)},
	}
	if promptReady(messages, "current") || promptReady(messages, "") || promptReady(messages, "missing") {
		t.Fatal("accepted completion without the current submission's answer")
	}
	messages = append(messages, wire.Message{Role: wire.MessageRoleAssistant, Content: "Current answer"})
	if !promptReady(messages, "current") {
		t.Fatal("submission identity was affected by repeated text or an old timestamp")
	}
}

func TestFinalAnswerRetainsPresentedArtifacts(t *testing.T) {
	messages := []wire.Message{{ID: "submission", Role: wire.MessageRoleUser, Content: "build"}, {Role: wire.MessageRoleAssistant, Content: "Built", ArtifactIDs: []string{"capture"}}}
	answer := finalAnswerMessage(messages)
	if answer == nil || len(answer.ArtifactIDs) != 1 || answer.ArtifactIDs[0] != "capture" {
		t.Fatalf("lost presented capture: %+v", answer)
	}
	messages = append(messages, wire.Message{Role: wire.MessageRoleUser, Content: "new task"})
	if finalAnswerMessage(messages) != nil {
		t.Fatal("presented a previous turn's capture as the new result")
	}
}

func TestSuiteRetainsResultsAndStopsOnModelMismatch(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "mismatch"}[mismatch], func(t *testing.T) {
			root, capture := t.TempDir(), t.TempDir()
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture"), 0o600))
			posts := 0
			var promptAt time.Time
			var serverMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serverMu.Lock()
				defer serverMu.Unlock()
				switch {
				case r.URL.Path == "/harness/execution/s/submission":
					_ = json.NewEncoder(w).Encode(session.ExecutionObservation{SessionID: "s", SubmissionID: "submission", SubmissionStatus: store.PromptSubmissionComplete, Settled: true})
				case r.URL.Path == "/v1/projects":
					_ = json.NewEncoder(w).Encode(wire.Project{ID: "p"})
				case r.URL.Path == "/v1/sessions":
					_ = json.NewEncoder(w).Encode(wire.Session{ID: "s"})
				case strings.HasSuffix(r.URL.Path, "/prompts"):
					posts++
					promptAt = time.Now()
					writeSuiteCapture(t, capture, "actual")
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "submission", MessageID: "submission"})
				case strings.HasSuffix(r.URL.Path, "/checkpoints"):
					_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{}})
				case strings.HasSuffix(r.URL.Path, "/workflow-runs/active"):
					_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
				case strings.HasSuffix(r.URL.Path, "/messages"):
					_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{Messages: []wire.Message{{ID: "submission", Role: wire.MessageRoleUser, Content: "inspect", CreatedAt: promptAt}, {Role: wire.MessageRoleAssistant, Content: "Useful answer"}}})
				default:
					_ = json.NewEncoder(w).Encode(wire.Session{ID: "s", Status: wire.SessionStatusIdle})
				}
			}))
			defer server.Close()
			model := "actual"
			if mismatch {
				model = "different"
			}
			suite := &Suite{ID: "suite", Digest: "digest", root: root, Cases: []SuiteCase{{CorpusTask: CorpusTask{ID: "case", Prompt: "inspect"}, Project: ".", ReadOnly: true, Outcomes: []string{"Useful answer"}}}}
			out := filepath.Join(t.TempDir(), "report.json")
			opts := SuiteOptions{LiveOptions: LiveOptions{AllowLive: true, BaseURL: server.URL, Token: "fixture", CaptureDir: capture, Runs: 1, Timeout: time.Second}, Suite: suite, ExpectedModel: model, Label: "test", Output: out}
			report, err := RunSuite(t.Context(), opts)
			if mismatch != (err != nil) {
				t.Fatalf("run error = %v, mismatch=%v, cases=%+v", err, mismatch, report.Cases)
			}
			if posts != 1 || len(report.Cases) != 1 || report.Cases[0].Final != "Useful answer" {
				t.Fatalf("lost case result: %+v", report)
			}
			stored, err := LoadSuiteReport(out)
			testutil.FailErr(t, "read retained report", err)
			if stored.PromptTokens != 22 {
				t.Fatalf("tokens=%d", stored.PromptTokens)
			}
			if !mismatch && stored.Cases[0].Status != "review_required" {
				t.Fatal("tool completion must not fabricate an outcome pass")
			}
			if _, err := RunSuite(t.Context(), opts); err == nil {
				t.Fatal("overwrote existing report")
			}
		})
	}
}

func writeSuiteCapture(t *testing.T, directory, model string) {
	t.Helper()
	record := logview.LLMRecord{SessionID: "s", AgentType: "coordinator", Surface: "implement_investigate", Call: "stream", TS: time.Now(), Model: model, Usage: &logview.LLMUsage{PromptTokens: 22}}
	body, err := json.Marshal(struct {
		logview.LLMRecord
		Completion map[string]any `json:"completion"`
	}{record, map[string]any{"content_chars": 13, "tool_calls": []map[string]any{{"name": "write"}}}})
	testutil.FailErr(t, "encode fixture capture", err)
	testutil.FailErr(t, "write fixture LLM capture", os.WriteFile(filepath.Join(directory, "llm-requests.jsonl"), append(body, '\n'), 0o600))
	testutil.FailErr(t, "write fixture session capture", os.WriteFile(filepath.Join(directory, "sessions.jsonl"), nil, 0o600))
}

func TestSuiteComparisonDoesNotCountUnreviewedAsSuccess(t *testing.T) {
	baseline := SuiteReport{SuiteSHA256: "suite", Label: "before", Cases: []CaseReport{{ID: "case", FixtureSHA256: "fixture", Status: "review_required"}}}
	candidate := baseline
	candidate.Label = "after"
	var output strings.Builder
	testutil.FailErr(t, "compare unreviewed outcomes", CompareSuites(&output, baseline, candidate))
	if !strings.Contains(output.String(), "passed=0") || !strings.Contains(output.String(), "unreviewed=1") {
		t.Fatal(output.String())
	}
	candidate.Cases = []CaseReport{{ID: "case", FixtureSHA256: "different"}}
	if CompareSuites(&output, baseline, candidate) == nil {
		t.Fatal("compared different fixtures")
	}
	candidate = baseline
	candidate.Cases = append([]CaseReport{}, baseline.Cases...)
	candidate.Cases = append(candidate.Cases, baseline.Cases[0])
	if CompareSuites(&output, baseline, candidate) == nil {
		t.Fatal("compared unequal repetition counts")
	}
}

func TestSuiteRefreshPreservesReviewAndIncludesLateCapture(t *testing.T) {
	capture := t.TempDir()
	writeSuiteCapture(t, capture, "actual")
	seedExecutionCapture(t, capture, "s")
	out := filepath.Join(t.TempDir(), "report.json")
	report := SuiteReport{Version: 1, SuiteID: "suite", CaptureDir: capture, ExpectedModel: "actual",
		Cases: []CaseReport{{ID: "case", SessionID: "s", Status: "review_required", Review: &OutcomeReview{Outcome: "passed", Reviewer: "tester", Notes: "Useful result"}}}}
	testutil.FailErr(t, "write provisional report", saveSuiteReport(out, report))
	testutil.FailErr(t, "refresh settled capture", RefreshSuiteReport(out))
	updated, err := LoadSuiteReport(out)
	testutil.FailErr(t, "read refreshed report", err)
	if updated.PromptTokens != 22 || updated.Cases[0].Review.Outcome != "passed" {
		t.Fatalf("refresh lost review or metrics: %+v", updated)
	}
}

func TestSuiteRefreshRejectsMissingExecutionLedger(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report.json")
	report := SuiteReport{Version: 1, SuiteID: "suite", CaptureDir: t.TempDir(), ExpectedModel: "actual", Cases: []CaseReport{{ID: "case", SessionID: "s", Status: "blocked"}}}
	testutil.FailErr(t, "save incomplete capture", saveSuiteReport(out, report))
	if err := RefreshSuiteReport(out); err == nil {
		t.Fatal("missing execution ledger was accepted")
	}
}

func TestProfileDisclosesMissingUsage(t *testing.T) {
	capture := t.TempDir()
	writeSuiteCapture(t, capture, "actual")
	path := filepath.Join(capture, "llm-requests.jsonl")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read captured call", err)
	var record map[string]any
	testutil.FailErr(t, "decode captured call", json.Unmarshal(body, &record))
	delete(record, "usage")
	record["error"] = "provider returned no content"
	missing, err := json.Marshal(record)
	testutil.FailErr(t, "encode missing usage", err)
	testutil.FailErr(t, "retain failed call", os.WriteFile(path, append(body, append(missing, '\n')...), 0o600))
	profile, err := ProfileFromCaptureSession(capture, "s")
	testutil.FailErr(t, "profile incomplete usage", err)
	if profile.TokenSpend.PromptTokens != 22 || profile.TokenSpend.MissingUsageCalls != 1 {
		t.Fatalf("missing usage silently treated as free: %+v", profile.TokenSpend)
	}
}

func TestSuiteAbortUsesOwnedSessionAndReportsRefusal(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/owned-session/abort" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Errorf("unexpected abort request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := &liveClient{base: server.URL, token: "fixture", http: server.Client()}
			if err := client.abortScenario(t.Context(), "owned-session"); (err != nil) != (status == http.StatusForbidden) {
				t.Fatalf("abort status %d: %v", status, err)
			}
		})
	}
}

func TestCacheHitRateUsesInclusiveInput(t *testing.T) {
	if got := cacheHitRate(100, 80); got != 0.8 {
		t.Fatalf("hit rate=%v, want 0.8", got)
	}
	if cacheHitRate(0, 0) != 0 || cacheHitRate(10, 20) != 1 || cacheHitRate(10, -1) != 0 {
		t.Fatal("invalid cache counts escaped bounds")
	}
}

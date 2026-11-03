package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMockEnabled(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")

	if MockEnabled(nil) {
		t.Fatal("expected mock disabled without test client or env")
	}
	if !MockEnabled(NewMockProvider(nil)) {
		t.Fatal("expected mock enabled when test client injected")
	}
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if !MockEnabled(nil) {
		t.Fatal("expected mock enabled when LYCAON_LLM_MOCK=1")
	}
}

func TestMockOnlyFromEnv(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	if MockOnlyFromEnv() {
		t.Fatal("expected false when unset")
	}
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if !MockOnlyFromEnv() {
		t.Fatal("expected true for 1")
	}
	t.Setenv("LYCAON_LLM_MOCK", "true")
	if !MockOnlyFromEnv() {
		t.Fatal("expected true for true")
	}
	t.Setenv("LYCAON_LLM_MOCK", "0")
	if MockOnlyFromEnv() {
		t.Fatal("expected false for 0")
	}
}

func TestProviderUtilityCallsEnabled(t *testing.T) {
	t.Setenv("LYCAON_DEV", "1")
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("LYCAON_LLM_MANUAL", "")
	if !ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls disabled outside test LLM modes")
	}
	t.Setenv("LYCAON_LLM_MANUAL", "1")
	if ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls enabled in manual harness mode")
	}
	t.Setenv("LYCAON_LLM_MANUAL", "")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls enabled in mock mode")
	}
}

func TestProviderRejectionRuleYAML(t *testing.T) {
	cfg, err := decodeProviderConfig([]byte(`providers:
  - id: any-provider
    rejection_reasons:
      - status: 403
        code_path: error.type
        code: payment_required
        reason: custom_payment_required
`))
	if err != nil {
		t.Fatalf("decode rejection rules: %v", err)
	}
	entry, err := resolveLocalEntry(cfg.Providers[0], nil)
	if err != nil {
		t.Fatalf("resolve rejection rules: %v", err)
	}
	// The common HTTP attempt retains structured data for every HTTP adapter.
	_, failure := providerretry.RunProviderAttempts(t.Context(), providerretry.ProviderAttempt{ //nolint:bodyclose // A failed attempt returns no response.
		ProviderID: entry.ID,
		Send: func(context.Context) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"type":"payment_required","message":"private diagnostics"}}`))}, nil
		},
	})
	got, ok := providerretry.AsProviderRequestRejected(rejectionReason(failure, entry.RejectionReasons))
	if !ok || got.Reason != "custom_payment_required" {
		t.Fatalf("custom provider mapping was not applied: %v", got)
	}
	copy := cloneCatalogEntry(entry)
	copy.RejectionReasons[0].Code = "changed"
	if entry.RejectionReasons[0].Code != "payment_required" {
		t.Fatal("catalog clone shared mutable rejection rules")
	}
}

func TestProviderRejectionRuleValidation(t *testing.T) {
	for _, rule := range []ProviderRejectionRule{
		{Status: 500, CodePath: "error.code", Code: "x", Reason: "x"},
		{Status: 403, CodePath: "error..code", Code: "x", Reason: "x"},
		{Status: 403, CodePath: "error.code", Reason: "x"},
	} {
		if validateRejectionRules([]ProviderRejectionRule{rule}) == nil {
			t.Fatalf("invalid rejection rule accepted: %+v", rule)
		}
	}
}

type namingProvider struct {
	namedStubProvider
	budgets []int
	failure error
}

func (p *namingProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.budgets = append(p.budgets, req.MaxTokens)
	if len(p.budgets) == 1 {
		return nil, p.failure
	}
	return &modelcall.Completion{Content: "Inspect source indexing"}, nil
}

func TestNamingRecoversOnlyStructuredOutputExhaustion(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		retries bool
	}{
		"reasoning consumed output": {&failure.ProviderEmptyCompletionError{Terminal: true, Reason: "length"}, true},
		"truncated output":          {&failure.ProviderOutputTruncatedError{}, true},
		"ordinary empty response":   {&failure.ProviderEmptyCompletionError{Terminal: true, Reason: "stop"}, false},
		"unstructured message":      {errors.New("length max_tokens"), false},
		"cancellation":              {context.Canceled, false},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &namingProvider{namedStubProvider: namedStubProvider{id: "lite"}, failure: tc.err}
			coordinator := &namedStubProvider{id: "coord"}
			summarizer := newFallbackTestSummarizer(t, provider, coordinator)
			summarizer.Plane = NewUtilityPlane()
			title, err := (PlaneNamer{Summarizer: summarizer}).Name(t.Context(), "Name the task", "Review indexing")
			if len(provider.budgets) == 0 || provider.budgets[0] < 1024 {
				t.Fatalf("reasoning has no completion allowance: %v", provider.budgets)
			}
			if tc.retries {
				if err != nil || title != "Inspect source indexing" || len(provider.budgets) != 2 || provider.budgets[1] <= provider.budgets[0] {
					t.Fatalf("recovery title=%q error=%v budgets=%v", title, err, provider.budgets)
				}
			} else if err == nil || len(provider.budgets) != 1 {
				t.Fatalf("unexpected replay: title=%q error=%v budgets=%v", title, err, provider.budgets)
			}
			if coordinator.calls != 0 {
				t.Fatal("naming spent coordinator capacity")
			}
		})
	}
}

// A Requested-class degrade must surface an error, never truncated text with
// a nil error — every caller reads a nil error as a real model answer.
func TestRequestedClassNeverReturnsTruncatedTextAsAnAnswer(t *testing.T) {
	prompt := strings.Repeat("secret-ish rationale prompt content. ", 40)
	r := &RegistrySummarizer{
		Fallback: compaction.TruncateSummarizer{},
		Class:    UtilityClassRequested,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", prompt, 64, true, UtilityClassRequested, errors.New("lite slot down"))
	if err == nil {
		t.Fatalf("degradeSummarize returned text with no error for a Requested class: %q", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("degradeSummarize returned %q; a forbidden-truncate class must carry no text", out)
	}
}

// Quality is the one class that may degrade to truncated input.
func TestQualityClassStillAllowsTruncateFallback(t *testing.T) {
	prompt := strings.Repeat("curation candidate. ", 40)
	r := &RegistrySummarizer{
		Fallback: compaction.TruncateSummarizer{},
		Class:    UtilityClassQuality,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", prompt, 64, true, UtilityClassQuality, errors.New("lite slot down"))
	if err != nil {
		t.Fatalf("degradeSummarize err = %v; Quality may truncate", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("Quality class produced no truncated text")
	}
}

// A non-truncating fallback still runs for a forbidden-truncate class.
func TestRequestedClassStillUsesNonTruncatingFallback(t *testing.T) {
	r := &RegistrySummarizer{
		Fallback: compaction.MockSummarizer{Text: "a real fallback answer"},
		Class:    UtilityClassRequested,
	}

	out, err := r.degradeSummarize(context.Background(), "sys", "prompt", 64, true, UtilityClassRequested, errors.New("lite slot down"))
	if err != nil {
		t.Fatalf("degradeSummarize err = %v; a non-truncating fallback must still run", err)
	}
	if out != "a real fallback answer" {
		t.Fatalf("out = %q want the fallback's answer", out)
	}
}

// UnavailableSummarizer carries no text a caller could mistake for output.
func TestUnavailableSummarizerReturnsErrorAndNoText(t *testing.T) {
	out, err := compaction.UnavailableSummarizer{}.Summarize(context.Background(), "sys", "prompt", 64)
	if err == nil {
		t.Fatal("UnavailableSummarizer returned a nil error")
	}
	if out != "" {
		t.Fatalf("out = %q want empty", out)
	}
}

func TestKickDrivenMockDispatchesByKickID(t *testing.T) {
	called := map[string]bool{}
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnPhaseAdvanced: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["phase-advanced"] = true
			return modelcall.Completion{Content: "ack-phase-advanced"}
		},
		OnLegFinished: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["leg-finished"] = true
			return modelcall.Completion{Content: "ack-leg"}
		},
		OnWorkerTaskFinished: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["worker-task-finished"] = true
			return modelcall.Completion{Content: "ack-worker"}
		},
		Fallback: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["fallback"] = true
			return modelcall.Completion{Content: "fallback"}
		},
	})

	cases := []struct {
		userMsg string
		want    string
	}{
		{"Phase advanced — review updated run context", "phase-advanced"},
		{"Leg finished — review the latest tagged worker summary", "leg-finished"},
		{"Worker task finished — review the tagged worker summary", "worker-task-finished"},
		{"random unrelated text", "fallback"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			out, err := mock.Complete(context.Background(), modelcall.CompletionRequest{
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: c.userMsg}},
			})
			testutil.FailErr(t, "Complete", err)
			if !called[c.want] {
				t.Fatalf("handler %q not called for %q (out=%+v)", c.want, c.userMsg, out)
			}
			called[c.want] = false // reset for next iteration
		})
	}
}

func TestKickDrivenMockPicksUpWorkerSummary(t *testing.T) {
	// The handler should see the latest worker_summary content via the snapshot
	// so tests can model "coordinator looked at the summary content and decided
	// to call task() again".
	var sawSummary string
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnWorkerTaskFinished: func(_ context.Context, snap Snapshot) modelcall.Completion {
			sawSummary = snap.LastWorkerSummary
			return modelcall.Completion{Content: "ok"}
		},
	})

	req := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleAssistant, Content: "<worker-summary>did the thing</worker-summary>",
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"}},
		{Role: api.MessageRoleUser, Content: "Worker task finished — review the tagged worker summary"},
	}}
	_, err := mock.Complete(context.Background(), req)
	testutil.FailErr(t, "Complete", err)

	if !strings.Contains(sawSummary, "did the thing") {
		t.Fatalf("OnWorkerTaskFinished did not see the worker summary; got %q", sawSummary)
	}
}

func TestKickDrivenMockStreamingReturnsContentAndToolCalls(t *testing.T) {
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnPhaseAdvanced: func(_ context.Context, _ Snapshot) modelcall.Completion {
			return modelcall.Completion{
				ToolCalls: []api.ToolCall{{ID: "t1", Name: "task", Args: map[string]any{"agent_type": "implementer"}}},
			}
		},
	})
	ch, err := mock.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Phase advanced — review"}},
	})
	testutil.FailErr(t, "Stream", err)
	var got modelcall.StreamChunk
	for chunk := range ch {
		got = chunk
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "task" {
		t.Fatalf("stream final chunk = %+v want task tool call", got)
	}
}

func TestKickDrivenSnapshotKeepsNewestTypedOutcomeIdentity(t *testing.T) {
	snapshot := buildSnapshot([]api.Message{
		{Content: "older", WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "old"}},
		{WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "new"}},
		{Role: api.MessageRoleUser, Content: "[host:loop-wake]"},
	})
	if snapshot.LastWorkerJobID != "new" || snapshot.LastWorkerSummary != "" {
		t.Fatalf("newest typed outcome lost its identity: %+v", snapshot)
	}
	plain := buildSnapshot([]api.Message{{Role: api.MessageRoleAssistant, Content: "Worker task finished: new"}})
	if plain.LastWorkerJobID != "" {
		t.Fatalf("prose invented worker identity: %+v", plain)
	}
}

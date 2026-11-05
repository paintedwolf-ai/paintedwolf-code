package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"
)

func testCompactionConfig() compaction.CompactionConfig {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	return cfg
}

func TestRegistrySummarizerRequestsCompactionSchema(t *testing.T) {
	provider := &captureProvider{id: "lite"}
	r := newTestRegistrySummarizer(t, provider)
	_, err := r.Summarize(compaction.WithSummaryFormat(context.Background()), "system", "transcript", 321)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if provider.last.ResponseFormat == nil || provider.last.ResponseFormat.Name != "compaction_summary" {
		t.Fatalf("response format = %+v", provider.last.ResponseFormat)
	}
	if provider.last.MaxTokens != 321 {
		t.Fatalf("MaxTokens = %d", provider.last.MaxTokens)
	}
	if len(provider.last.Messages) != 2 || !transcript.HasAuthorityNotice(provider.last.Messages[0]) {
		t.Fatalf("messages = %+v", provider.last.Messages)
	}
}

func TestCompactionResultDoesNotDependOnAccounting(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 1000000
	cfg.KeepRecentMessages = 1
	input := []compaction.ContextMessage{{ID: "charter", Role: "user", Content: "Worker scope", ContextPinned: true},
		{ID: "old", Role: "user", Content: strings.Repeat("old evidence ", 2000)},
		{ID: "recent", Role: "assistant", Content: "Continue"}}
	var wantSummary string
	summary, err := (compaction.MockSummarizer{Text: "Continue the same task."}).Summarize(compaction.WithSummaryFormat(t.Context()), "", "", 0)
	testutil.FailErr(t, "encode continuation fixture", err)
	for _, accounting := range []struct {
		name    string
		tracker cost.CostTracker
	}{
		{"disabled", nil},
		{"healthy", &receiptLedgerTracker{}},
		{"price failure", &receiptLedgerTracker{estimateErr: errors.New("no prices")}},
		{"ledger failure", &receiptLedgerTracker{beginErr: errors.New("storage unavailable"), recordErr: errors.New("storage unavailable")}},
	} {
		t.Run(accounting.name, func(t *testing.T) {
			provider := &costUsageProvider{stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{summary}},
				usage: modelcall.TokenUsage{PromptTokens: 50, CompletionTokens: 20}}
			summarizer := newTestRegistrySummarizer(t, provider)
			summarizer.Cost = accounting.tracker
			compactor := compaction.NewSimpleCompactor(cfg, summarizer)
			out, report, err := compactor.Compact(t.Context(), compaction.SessionInfo{ID: "session"}, input, 200)
			testutil.FailErr(t, "compact with accounting state", err)
			if !report.SessionCompacted || len(out) != 4 || out[2].ID != "charter" || out[2].Content != "Worker scope" {
				t.Fatalf("compaction result = %+v, report=%+v", out, report)
			}
			if wantSummary == "" {
				wantSummary = out[1].Content
			}
			if out[1].Content != wantSummary {
				t.Fatal("accounting changed the continuation")
			}
		})
	}
}

type namedStubProvider struct {
	id      string
	content string
	err     error
	calls   int
}

func (s *namedStubProvider) ID() string { return s.id }

func (s *namedStubProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &modelcall.Completion{Content: s.content}, nil
}

func (s *namedStubProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		c, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: c.Content, Done: true}
	}()
	return ch, nil
}

func (s *namedStubProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: s.id + "-model"}}
}

func (s *namedStubProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func newFallbackTestSummarizer(t *testing.T, lite, coord modelcall.Provider) *RegistrySummarizer {
	t.Helper()
	policy := NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: coord.ID(), Model: coord.ID() + "-model"},
		Lite:        ModelRef{ProviderID: lite.ID(), Model: lite.ID() + "-model"},
	})

	reg := newTestRegistry()
	testutil.FailErr(t, "Register lite", reg.Register(lite))
	if coord.ID() != lite.ID() {
		testutil.FailErr(t, "Register coord", reg.Register(coord))
	}

	return &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Scope:    SettingsScopeGlobal,
		Fallback: compaction.TruncateSummarizer{},
	}
}

func TestSummarizeFallsBackToCoordinatorOnLiteFailure(t *testing.T) {
	lite := &namedStubProvider{id: "lite", err: errors.New("timeout awaiting response headers")}
	coord := &namedStubProvider{id: "coord", content: "coordinator summary"}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "user prompt", 128)
	testutil.FailErr(t, "Summarize", err)
	if got != "coordinator summary" {
		t.Fatalf("summary = %q, want coordinator summary", got)
	}
	if lite.calls != 1 || coord.calls != 1 {
		t.Fatalf("calls lite=%d coord=%d, want 1/1", lite.calls, coord.calls)
	}
}

func TestSummarizeSkipsCoordinatorRetryWhenShared(t *testing.T) {
	shared := &namedStubProvider{id: "coord", err: errors.New("provider down")}
	r := newFallbackTestSummarizer(t, shared, shared)

	got, err := r.Summarize(context.Background(), "sys", "user prompt body", 128)
	testutil.FailErr(t, "Summarize", err)
	if shared.calls != 1 {
		t.Fatalf("shared provider calls = %d, want 1 (no self-retry)", shared.calls)
	}
	if got == "" {
		t.Fatal("expected truncate fallback content, got empty")
	}
}

func TestSummarizeUsesLiteWhenHealthy(t *testing.T) {
	lite := &namedStubProvider{id: "lite", content: "lite summary"}
	coord := &namedStubProvider{id: "coord", content: "coordinator summary"}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "user prompt", 128)
	testutil.FailErr(t, "Summarize", err)
	if got != "lite summary" {
		t.Fatalf("summary = %q, want lite summary", got)
	}
	if coord.calls != 0 {
		t.Fatalf("coordinator called %d times on healthy lite path", coord.calls)
	}
}

package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

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

	reg := newEmptyRegistry()
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

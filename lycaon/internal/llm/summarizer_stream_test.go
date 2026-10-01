package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// chunkedProvider streams a completion in fixed fragments.
type chunkedProvider struct {
	captureProvider
	fragments     []string
	streamErr     error
	completeCalls int
}

func (c *chunkedProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.completeCalls++
	return c.captureProvider.Complete(ctx, req)
}

func (c *chunkedProvider) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.last = req
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	ch := make(chan modelcall.StreamChunk, len(c.fragments)+1)
	for _, f := range c.fragments {
		ch <- modelcall.StreamChunk{Content: f}
	}
	ch <- modelcall.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

func TestSummarizeStreamDeliversDeltasAndFullContent(t *testing.T) {
	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		fragments:       []string{`{"seeds":["https://a.exam`, `ple"],"leads":[]}`},
	}
	r := newTestRegistrySummarizer(t, provider)

	var deltas []string
	out, err := r.SummarizeStream(context.Background(), "system", "user", 100, func(d string) {
		deltas = append(deltas, d)
	})
	testutil.FailErr(t, "SummarizeStream", err)
	if out != `{"seeds":["https://a.example"],"leads":[]}` {
		t.Fatalf("out = %q", out)
	}
	if len(deltas) != 2 || strings.Join(deltas, "") != out {
		t.Fatalf("deltas = %q want full content in order", deltas)
	}
	content := provider.last.Messages[0].Content
	if !strings.Contains(content, "Today is ") || provider.last.Think != modelcall.ThinkOff {
		t.Fatalf("request = %+v want TodayLine + ThinkOff parity with Summarize", provider.last)
	}
	if !transcript.HasAuthorityNotice(provider.last.Messages[0]) || provider.last.Messages[1].Content != "user" {
		t.Fatalf("provenance projection = %+v", provider.last.Messages)
	}
	if provider.last.MaxTokens != 100 {
		t.Fatalf("max tokens = %d, want 100", provider.last.MaxTokens)
	}
}

func TestSummarizeStreamOnceReturnsSetupFailure(t *testing.T) {
	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		streamErr:       fmt.Errorf("no stream"),
	}
	r := newTestRegistrySummarizer(t, provider)
	if _, err := r.SummarizeStreamOnce(context.Background(), "system", "user", 100, nil); err == nil {
		t.Fatal("one-attempt stream unexpectedly succeeded")
	}
	if provider.completeCalls != 0 {
		t.Fatalf("blocking calls = %d, want none", provider.completeCalls)
	}
}

func TestSummarizeStreamFallsBackToCompleteOnSetupError(t *testing.T) {
	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		streamErr:       fmt.Errorf("no stream"),
	}
	r := newTestRegistrySummarizer(t, provider)
	out, err := r.SummarizeStream(context.Background(), "system", "user", 100, nil)
	testutil.FailErr(t, "SummarizeStream", err)
	if out != "ok" {
		t.Fatalf("out = %q want blocking-call fallback content", out)
	}
}

func TestSummarizeStreamOpensCircuitOnSilence(t *testing.T) {
	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		streamErr:       &failure.ProviderSilentError{ProviderID: "lite", Model: "lite", Attempts: 1},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Plane = NewUtilityPlane()
	r.Purpose = "file_briefing"
	r.Class = UtilityClassRequested

	_, err := r.SummarizeStreamOnce(context.Background(), "system", "user", 100, nil)
	if _, ok := failure.AsProviderSilent(err); !ok {
		t.Fatalf("err = %v want silence", err)
	}
	if r.Plane.allowLite() {
		t.Fatal("a silent stream must open the circuit")
	}

	provider.streamErr = nil
	provider.fragments = []string{"should not run"}
	_, err = r.SummarizeStreamOnce(context.Background(), "system", "user", 100, nil)
	if !errors.Is(err, ErrLiteUnavailable) {
		t.Fatalf("second err = %v want unavailable", err)
	}
}

func TestStreamLitePublishesLaneOccupiedCall(t *testing.T) {
	life := &recordingLifecycle{}
	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		fragments:       []string{"hello", " world"},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Lifecycle = life

	out, err := r.SummarizeStream(occupiedSessionCtx(), "system", "user", 100, nil)
	testutil.FailErr(t, "SummarizeStream", err)
	if out != "hello world" {
		t.Fatalf("out = %q", out)
	}
	if len(life.evs) != 2 {
		t.Fatalf("events = %d want 2: %+v", len(life.evs), life.evs)
	}
	if life.evs[0].Status != api.LLMCallStatusActive || life.evs[1].Status != api.LLMCallStatusOK {
		t.Fatalf("events = %+v", life.evs)
	}
	if life.evs[0].CallID == "" || life.evs[0].CallID != life.evs[1].CallID {
		t.Fatalf("call id mismatch: %+v", life.evs)
	}
}

func TestUtilityTransportsCarryProjectOnlyScreeningIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			provider := &chunkedProvider{captureProvider: captureProvider{id: "lite"}, fragments: []string{"summary"}}
			summarizer := newTestRegistrySummarizer(t, provider)
			summarizer.ProjectID, summarizer.ProjectDir, summarizer.Purpose = "project-only", "/project-only", "file_briefing"
			var err error
			if stream {
				_, err = summarizer.SummarizeStreamOnce(t.Context(), "system", "user", 100, nil)
			} else {
				_, err = summarizer.Summarize(t.Context(), "system", "user", 100)
			}
			testutil.FailErr(t, "send utility request", err)
			request := provider.last
			if request.Composition != modelcall.CompositionHostUtility || request.Debug.ProjectID != "project-only" ||
				request.Debug.ProjectDir != "/project-only" || request.Debug.Purpose != "file_briefing" || request.Debug.SessionID != "" {
				t.Fatalf("utility request attribution = %+v; composition=%q", request.Debug, request.Composition)
			}
		})
	}
}

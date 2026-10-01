package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWrapLLMClientIfDebugWritesCapture(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	inner := NewMockProvider(&MockConfig{Responses: []MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	client := WrapLLMClientIfDebug(inner, "mock")
	_, err := client.Complete(t.Context(), modelcall.CompletionRequest{
		Model: "mock",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var entry struct {
		ProviderID string        `json:"provider_id"`
		Call       string        `json:"call"`
		DurationMs int64         `json:"duration_ms"`
		TTFTMs     int64         `json:"ttft_ms"`
		Messages   []api.Message `json:"messages"`
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.ProviderID != "mock" || entry.Call != "complete" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.TTFTMs != entry.DurationMs {
		t.Fatalf("timing = duration %d ttft %d", entry.DurationMs, entry.TTFTMs)
	}
}

func TestWrapLLMClientIfDebugCapturesStreamTiming(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	inner := &delayedStreamProvider{firstDelay: 25 * time.Millisecond}
	client := WrapLLMClientIfDebug(inner, "mock")
	ch, err := client.Stream(t.Context(), modelcall.CompletionRequest{
		Model: "mock",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_, _, _ = modelcall.CollectStream(ch)

	var data []byte
	testutil.WaitFor(t, 2*time.Second, func() bool {
		var err error
		data, err = os.ReadFile(logPath)
		return err == nil && len(data) > 0
	})
	var entry struct {
		Call       string `json:"call"`
		DurationMs int64  `json:"duration_ms"`
		TTFTMs     int64  `json:"ttft_ms"`
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Call != "stream" {
		t.Fatalf("call = %q", entry.Call)
	}
	if entry.TTFTMs <= 0 || entry.DurationMs <= entry.TTFTMs {
		t.Fatalf("timing = duration %d ttft %d want ttft < duration", entry.DurationMs, entry.TTFTMs)
	}
}

func TestLoggingProviderWrapsRegistryProviders(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	p := &recordingProvider{id: "test-provider"}
	wrapped := wrapProviderIfDebug(p)
	if _, ok := wrapped.(*loggingProvider); !ok {
		t.Fatalf("expected loggingProvider, got %T", wrapped)
	}
	_, err := wrapped.Complete(t.Context(), modelcall.CompletionRequest{
		Model: "m1",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "ping",
		}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if p.last.Model != "m1" {
		t.Fatalf("inner model = %q", p.last.Model)
	}
}

type recordingProvider struct {
	id   string
	last modelcall.CompletionRequest
}

func (p *recordingProvider) ID() string { return p.id }

func (p *recordingProvider) Models() []modelcall.ModelInfo { return nil }

func (p *recordingProvider) Profile() providerprofile.Profile { return providerprofile.OpenAI() }

func (p *recordingProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.last = req
	return &modelcall.Completion{Content: "ok"}, nil
}

func (p *recordingProvider) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.last = req
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

type delayedStreamProvider struct {
	firstDelay time.Duration
}

func (p *delayedStreamProvider) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "ok"}, nil
}

func (p *delayedStreamProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		time.Sleep(p.firstDelay)
		ch <- modelcall.StreamChunk{Content: "tok"}
		time.Sleep(20 * time.Millisecond)
		ch <- modelcall.StreamChunk{Done: true}
	}()
	return ch, nil
}

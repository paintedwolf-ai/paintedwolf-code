package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCaptureLLMCompleteWritesTiming(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "complete.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	req := modelcall.CompletionRequest{Model: "mock", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}}
	if _, err := captureLLMComplete("mock", providerprofile.Default(), req, func() (*modelcall.Completion, error) {
		time.Sleep(5 * time.Millisecond)
		return &modelcall.Completion{Content: "ok"}, nil
	}); err != nil {
		t.Fatalf("captureLLMComplete: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("read log: %v len=%d", err, len(data))
	}
}

// A utility complete with a Purpose label logs one self-attributing row under
// that label — no second feature-level row.
func TestCaptureLLMCompletePurposeLabel(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "purpose.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	req := modelcall.CompletionRequest{
		Model:    "mock",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug: modelcall.RequestDebug{
			SessionID: "sess-1",
			AgentType: "explore_readonly",
			Purpose:   "curate",
		},
	}
	if _, err := captureLLMComplete("mock", providerprofile.Default(), req, func() (*modelcall.Completion, error) {
		return &modelcall.Completion{Content: "ok", Usage: modelcall.TokenUsage{PromptTokens: 7, CompletionTokens: 3}}, nil
	}); err != nil {
		t.Fatalf("captureLLMComplete: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("read log: %v len=%d", err, len(data))
	}
	var entry struct {
		Call      string `json:"call"`
		SessionID string `json:"session_id"`
		AgentType string `json:"agent_type"`
		Usage     *struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Call != "curate" {
		t.Fatalf("call = %q want curate", entry.Call)
	}
	if entry.SessionID != "sess-1" || entry.AgentType != "explore_readonly" {
		t.Fatalf("attribution = %q/%q", entry.SessionID, entry.AgentType)
	}
	if entry.Usage == nil || entry.Usage.PromptTokens != 7 {
		t.Fatalf("usage = %+v want prompt_tokens 7", entry.Usage)
	}
}

func TestCaptureLLMStreamWritesTimingAfterCollect(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	req := modelcall.CompletionRequest{
		Model: "mock",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "hello",
		}},
	}
	ch, err := captureLLMStream(t.Context(), "mock", providerprofile.Default(), req, func() (<-chan modelcall.StreamChunk, error) {
		inner := make(chan modelcall.StreamChunk, 2)
		go func() {
			defer close(inner)
			time.Sleep(20 * time.Millisecond)
			inner <- modelcall.StreamChunk{Content: "tok"}
			time.Sleep(15 * time.Millisecond)
			inner <- modelcall.StreamChunk{Done: true}
		}()
		return inner, nil
	})
	if err != nil {
		t.Fatalf("captureLLMStream: %v", err)
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
		t.Fatalf("timing = duration %d ttft %d", entry.DurationMs, entry.TTFTMs)
	}
}

// A stream that never opens (provider HTTP/dial error) must still leave a capture entry.
func TestCaptureLLMStreamRecordsOpenError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream-open-err.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	req := modelcall.CompletionRequest{Model: "mock", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}}
	_, err := captureLLMStream(t.Context(), "mock", providerprofile.Default(), req, func() (<-chan modelcall.StreamChunk, error) {
		return nil, errors.New("ollama: chat HTTP 500: model overloaded")
	})
	if err == nil {
		t.Fatal("expected stream open error")
	}
	if got := captureErrorField(t, logPath); got != "ollama: chat HTTP 500: model overloaded" {
		t.Fatalf("captured error = %q", got)
	}
}

// A forwarded mid-stream error chunk must be logged as an error, not a clean success.
func TestCaptureLLMStreamRecordsMidStreamError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream-mid-err.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	req := modelcall.CompletionRequest{Model: "mock", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}}
	ch, err := captureLLMStream(t.Context(), "mock", providerprofile.Default(), req, func() (<-chan modelcall.StreamChunk, error) {
		inner := make(chan modelcall.StreamChunk, 2)
		go func() {
			defer close(inner)
			inner <- modelcall.StreamChunk{Content: "tok"}
			inner <- modelcall.StreamChunk{Err: errors.New("ollama: unexpected EOF"), Done: true}
		}()
		return inner, nil
	})
	if err != nil {
		t.Fatalf("captureLLMStream: %v", err)
	}
	for range ch {
	}
	var got string
	testutil.WaitFor(t, 2*time.Second, func() bool {
		got = captureErrorField(t, logPath)
		return got != ""
	})
	if got != "ollama: unexpected EOF" {
		t.Fatalf("captured error = %q", got)
	}
}

// A RegistrySummarizer with Purpose set labels its blocking capture row with
// that purpose instead of the generic "complete".
func TestRegistrySummarizerPurposeLabelsCompleteCapture(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "summarizer-complete.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	provider := &captureProvider{id: "lite"}
	r := newTestRegistrySummarizer(t, wrapProviderIfDebug(provider))
	r.Purpose = "compaction"

	_, err := r.Summarize(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if provider.last.Debug.Purpose != "compaction" {
		t.Fatalf("request purpose = %q want compaction", provider.last.Debug.Purpose)
	}
	if got := captureCallField(t, logPath); got != "compaction" {
		t.Fatalf("call = %q want compaction", got)
	}
}

// A RegistrySummarizer with Purpose set labels its stream capture row with
// that purpose instead of the generic "stream".
func TestRegistrySummarizerPurposeLabelsStreamCapture(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "summarizer-stream.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()

	provider := &chunkedProvider{
		captureProvider: captureProvider{id: "lite"},
		fragments:       []string{"streamed summary"},
	}
	r := newTestRegistrySummarizer(t, wrapProviderIfDebug(provider))
	r.Purpose = "seed_pick"

	out, err := r.SummarizeStream(context.Background(), "system", "user", 100, nil)
	testutil.FailErr(t, "SummarizeStream", err)
	if out != "streamed summary" {
		t.Fatalf("out = %q", out)
	}
	if provider.last.Debug.Purpose != "seed_pick" {
		t.Fatalf("request purpose = %q want seed_pick", provider.last.Debug.Purpose)
	}
	// The stream capture row is written asynchronously after the provider
	// channel drains.
	var got string
	testutil.WaitFor(t, 2*time.Second, func() bool {
		got = captureCallField(t, logPath)
		return got != ""
	})
	if got != "seed_pick" {
		t.Fatalf("call = %q want seed_pick", got)
	}
}

func captureCallField(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	var entry struct {
		Call string `json:"call"`
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}
	return entry.Call
}

func captureErrorField(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	var entry struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}
	return entry.Error
}

func TestCaptureEmptyCompletionRetainsProtocolFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", path)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)
	_, responseErr := captureLLMComplete("fixture", providerprofile.Default(), modelcall.CompletionRequest{Model: "candidate"}, func() (*modelcall.Completion, error) {
		return nil, &failure.ProviderEmptyCompletionError{Terminal: true, Retryable: false, Attempts: 2, Reason: "content_filter"}
	})
	if responseErr == nil {
		t.Fatal("provider failure disappeared")
	}
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read protocol capture", err)
	var entry struct {
		Empty *observability.LLMEmptyCompletionCapture `json:"empty_completion"`
	}
	testutil.FailErr(t, "decode protocol capture", json.Unmarshal(data, &entry))
	if entry.Empty == nil || !entry.Empty.Terminal || entry.Empty.Retryable || entry.Empty.Attempts != 2 || entry.Empty.Reason != "content_filter" {
		t.Fatalf("protocol facts = %+v", entry.Empty)
	}
}

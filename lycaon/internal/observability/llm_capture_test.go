package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLLMDebugEnabled(t *testing.T) {
	t.Setenv("LYCAON_LLM_DEBUG", "")
	CloseLLMDebug()
	if LLMDebugEnabled() {
		t.Fatal("expected disabled by default")
	}
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	if !LLMDebugEnabled() {
		t.Fatal("expected enabled for 1")
	}
	t.Setenv("LYCAON_LLM_DEBUG", "true")
	if !LLMDebugEnabled() {
		t.Fatal("expected enabled for true")
	}
}

func TestLogLLMRequestWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	CloseLLMDebug()

	LogLLMRequest("mock", "mock", "complete", []api.Message{{
		Role:    api.MessageRoleUser,
		Content: "hello",
	}}, nil, LLMRequestTiming{DurationMs: 42, TTFTMs: 42}, LLMUsageCapture{
		PromptTokens: 10, CompletionTokens: 2, CacheReadInputTokens: 5,
	}, LLMRequestDebug{SessionID: "sess-1", ProfileID: "coordinator", HostTurn: true, Surface: "synthesis", Iteration: 2, MaxIterations: 8}, nil, "")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var entry llmCaptureEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.ProviderID != "mock" {
		t.Fatalf("provider_id = %q", entry.ProviderID)
	}
	if entry.Call != "complete" {
		t.Fatalf("call = %q", entry.Call)
	}
	if len(entry.Messages) != 1 || entry.Messages[0].Content != "hello" {
		t.Fatalf("messages = %+v", entry.Messages)
	}
	if entry.DurationMs != 42 || entry.TTFTMs != 42 {
		t.Fatalf("timing = duration %d ttft %d", entry.DurationMs, entry.TTFTMs)
	}
	if entry.SessionID != "sess-1" || entry.Surface != "synthesis" || !entry.HostTurn {
		t.Fatalf("debug meta = session %q surface %q host %v", entry.SessionID, entry.Surface, entry.HostTurn)
	}
}

func TestLogLLMRequestWorkerIdentityAndManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", filepath.Join(dir, "requests.jsonl"))
	CloseLLMDebug()

	coord := LLMRequestDebug{SessionID: "coord-1", ProfileID: "coordinator", Surface: "investigate", Iteration: 1, MaxIterations: 8}
	worker := LLMRequestDebug{SessionID: "worker-1", AgentType: "web-researcher", ParentSessionID: "coord-1", ProfileID: "web_research", Iteration: 1, MaxIterations: 30}

	LogLLMRequest("mock", "mock", "stream", []api.Message{{Role: api.MessageRoleUser, Content: "Survey this repo."}}, nil, LLMRequestTiming{}, LLMUsageCapture{}, coord, nil, "")
	LogLLMRequest("mock", "mock", "stream", []api.Message{{Role: api.MessageRoleUser, Content: "Leg assignment: research shells."}}, nil, LLMRequestTiming{}, LLMUsageCapture{}, worker, nil, "")
	// A second turn for the worker must not add a second manifest row.
	LogLLMRequest("mock", "mock", "stream", []api.Message{{Role: api.MessageRoleUser, Content: "Leg assignment: research shells."}}, nil, LLMRequestTiming{}, LLMUsageCapture{}, worker, nil, "")

	// Worker identity lands on the request rows.
	rows := readJSONL[llmCaptureEntry](t, filepath.Join(dir, "requests.jsonl"))
	if len(rows) != 3 {
		t.Fatalf("want 3 request rows, got %d", len(rows))
	}
	if rows[1].AgentType != "web-researcher" || rows[1].ParentSessionID != "coord-1" {
		t.Fatalf("worker row identity = agent %q parent %q", rows[1].AgentType, rows[1].ParentSessionID)
	}

	// The manifest holds exactly one row per session, with the task summary.
	manifest := readJSONL[sessionManifestEntry](t, filepath.Join(dir, debugpaths.Name(debugpaths.KindSessions)))
	if len(manifest) != 2 {
		t.Fatalf("want 2 manifest rows (one per session), got %d", len(manifest))
	}
	if manifest[0].SessionID != "coord-1" || manifest[0].Task != "Survey this repo." {
		t.Fatalf("coordinator manifest = %+v", manifest[0])
	}
	if manifest[1].SessionID != "worker-1" || manifest[1].AgentType != "web-researcher" || manifest[1].ParentSessionID != "coord-1" {
		t.Fatalf("worker manifest = %+v", manifest[1])
	}
}

func TestLogLLMRequestWritesCompletionSummary(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	CloseLLMDebug()

	summary := SummarizeLLMCompletion("", []api.ToolCall{{
		Name: "update_progress",
		Args: map[string]any{"content": "- [ ] Core library\n"},
	}})
	LogLLMRequest("mock", "mock", "stream", nil, nil, LLMRequestTiming{}, LLMUsageCapture{
		CompletionTokens: 8007,
	}, LLMRequestDebug{}, summary, "")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var entry llmCaptureEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Completion == nil || len(entry.Completion.ToolCalls) != 1 {
		t.Fatalf("completion = %+v", entry.Completion)
	}
	if entry.Completion.HiddenTokenEstimate <= 0 {
		t.Fatalf("hidden_token_estimate = %d", entry.Completion.HiddenTokenEstimate)
	}
}

func TestSummarizeLLMCompletion(t *testing.T) {
	summary := SummarizeLLMCompletion("hello", nil)
	if summary == nil || summary.ContentChars != 5 || summary.EstimatedVisibleTokens <= 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func readJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []T
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("unmarshal %s row: %v", path, err)
		}
		out = append(out, v)
	}
	return out
}

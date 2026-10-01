package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

func TestLogPromptCacheObservabilityWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	llmPath := filepath.Join(dir, "llm-requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", llmPath)
	CloseLLMDebug()

	LogPromptCacheObservability(PromptCacheObservation{
		PromptCacheScope: PromptCacheScope{SessionID: "sess-1", ProviderID: "provider", Model: "model", Purpose: "stream"},
		Time:             time.Now(), CallID: "call-1", PromptCache: "explicit_breakpoints", Comparison: "comparable",
		Window: PromptCacheWindow{Requests: 3, InputTokens: 24000}, Alert: "no_reads_reported", Warn: true,
	})

	path := filepath.Join(dir, debugpaths.Name(debugpaths.KindPromptCache))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read observability log: %v", err)
	}
	var entry PromptCacheObservation
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.SessionID != "sess-1" || entry.PromptCache != "explicit_breakpoints" || entry.ProviderID != "provider" || entry.Model != "model" || entry.Purpose != "stream" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.CallID != "call-1" || entry.Window.Requests != 3 || entry.Alert != "no_reads_reported" {
		t.Fatalf("lost comparison evidence: %+v", entry)
	}
}

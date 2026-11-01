package observability

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparationCaptureDoesNotClaimTheSessionTask(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", path)
	CloseLLMDebug()
	t.Cleanup(CloseLLMDebug)
	LogLLMPreparation("local", "model", "preparation-session", []api.Message{{Role: api.MessageRoleSystem, Content: "Known context"}}, nil, time.Second, "")
	rows := readJSONL[llmCaptureEntry](t, path)
	if len(rows) != 1 || rows[0].Call != "prepare" || rows[0].DurationMs != 1000 || rows[0].Usage != nil {
		t.Fatalf("preparation capture = %+v", rows)
	}
	LogLLMRequest("local", "model", "stream", []api.Message{{Role: api.MessageRoleUser, Content: "Actual request"}}, nil, LLMRequestTiming{}, LLMUsageCapture{}, LLMRequestDebug{SessionID: "preparation-session"}, nil, "")
	manifest := readJSONL[sessionManifestEntry](t, filepath.Join(dir, debugpaths.Name(debugpaths.KindSessions)))
	if len(manifest) != 1 || manifest[0].Task != "Actual request" {
		t.Fatalf("preparation consumed task identity: %+v", manifest)
	}
}

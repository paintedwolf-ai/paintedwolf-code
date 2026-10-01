package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaBuildRequestKeepAlive(t *testing.T) {
	p := New("ollama", "http://unused", "", []modelinfo.Entry{{ID: "llama3.1", ContextLength: 8192}}).
		WithPromptCache(providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheLocalKV, KeepAlive: "24h"})
	req := modelcall.CompletionRequest{
		Model:    "llama3.1",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	}
	body, _, err := p.Prepare(t.Context(), req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if body.KeepAlive != "24h" {
		t.Fatalf("keep_alive = %q, want the policy's 24h", body.KeepAlive)
	}
}

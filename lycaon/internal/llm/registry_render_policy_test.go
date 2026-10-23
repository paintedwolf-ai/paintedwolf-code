package llm

import (
	"encoding/json"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOfflineRenderUsesServingThinkingPolicy(t *testing.T) {
	catalog := mustCatalogCloneShipToLocal(t, `providers:
  - id: student
    kind: openai-compatible
    base_url: http://127.0.0.1:9/v1
    api_key_env: ""
    models:
      - id: e4b
        thinking: {state: supported, efforts: [low]}
`+MinimalShipHTTPRetryYAML)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credentials.age")))
	testutil.FailErr(t, "create render registry", err)
	// A teacher's prepared native override must not cross the serving boundary.
	req := modelcall.CompletionRequest{Think: modelcall.ThinkHigh, ThinkingOverride: &modelcall.ThinkingOverride{Mode: "fixed", Effort: "high"}}
	student := modelcall.ThinkingOverride{ProviderID: "student", Model: "e4b", Mode: "fixed", Effort: "low"}
	registry.thinkingPolicy.Store(NewInMemoryPolicyStore(ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{student}}))
	_, body, err := registry.RenderRequest(t.Context(), "student", "e4b", req)
	testutil.FailErr(t, "render student policy without contacting server", err)
	var wire map[string]any
	testutil.FailErr(t, "decode student render", json.Unmarshal(body, &wire))
	if wire["reasoning_effort"] != "low" {
		t.Fatalf("student policy lost: %s", body)
	}
	ctx := WithThinkingPolicy(t.Context(), ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "student", Model: "e4b", Mode: "application"}}})
	_, body, err = registry.RenderRequest(ctx, "student", "e4b", req)
	testutil.FailErr(t, "render application policy", err)
	testutil.FailErr(t, "decode application render", json.Unmarshal(body, &wire))
	if wire["reasoning_effort"] != "high" {
		t.Fatalf("application effort lost: %s", body)
	}
	student.Effort = "unsupported"
	ctx = WithThinkingPolicy(t.Context(), ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{student}})
	if _, _, err = registry.RenderRequest(ctx, "student", "e4b", req); err == nil {
		t.Fatal("unsupported serving override rendered")
	}
}

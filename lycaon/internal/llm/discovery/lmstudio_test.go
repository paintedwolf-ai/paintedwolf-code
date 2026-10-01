package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverLMStudioModelsKeepsLLMandVLM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "meta-llama-3.1-8b-instruct", "type": "llm", "capabilities": map[string]any{"trained_for_tool_use": true}},
				{"id": "qwen2-vl-7b", "type": "vlm"},
				{
					// Embedding model: no chat/completions support.
					"id":   "text-embedding-nomic-embed-text-v1.5",
					"type": "embeddings",
				},
			},
		})
	}))
	defer srv.Close()

	models, err := LMStudioModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "discover lmstudio models", err)
	if len(models) != 2 {
		t.Fatalf("models = %+v, want llm+vlm (drop embeddings)", models)
	}
	byID := map[string]modelinfo.Entry{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if _, ok := byID["meta-llama-3.1-8b-instruct"]; !ok {
		t.Fatalf("missing llm: %+v", models)
	}
	if !modelinfo.Supported(byID["meta-llama-3.1-8b-instruct"].EffectiveCapabilities().Tools) {
		t.Fatalf("llm tool capability = %+v", byID["meta-llama-3.1-8b-instruct"].Capabilities.Tools)
	}
	vlm, ok := byID["qwen2-vl-7b"]
	if !ok || !modelinfo.Supported(vlm.Capabilities.Vision) {
		t.Fatalf("vlm = %+v, want vision support", models)
	}
	if vlm.Capabilities.Tools.State != modelinfo.CapabilityUnknown {
		t.Fatalf("absent tool metadata = %+v, want unknown", vlm.Capabilities.Tools)
	}
}

func TestLMStudioNativeBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:1234/v1":  "http://localhost:1234",
		"http://localhost:1234/v1/": "http://localhost:1234",
		"http://localhost:1234":     "http://localhost:1234",
	}
	for in, want := range cases {
		if got := lmstudioNativeBase(in); got != want {
			t.Fatalf("lmstudioNativeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

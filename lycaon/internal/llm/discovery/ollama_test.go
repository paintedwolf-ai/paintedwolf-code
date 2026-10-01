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

func TestDiscoverOllamaModelsCompletionOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "llama3.1:latest"}, {"name": "nomic-embed-text:latest"}},
			})
		case "/api/show":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			capabilities := []string{"embedding"}
			if req["model"] == "llama3.1:latest" {
				capabilities = []string{"completion", "tools"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	models, err := OllamaModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "discover ollama models", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want 1 completion-capable entry (drop embedding-only)", models)
	}
	if models[0].ID != "llama3.1:latest" {
		t.Fatalf("model = %+v", models[0])
	}
	if !modelinfo.Supported(models[0].Capabilities.Tools) {
		t.Fatalf("tools capability = %+v", models[0].Capabilities.Tools)
	}
}

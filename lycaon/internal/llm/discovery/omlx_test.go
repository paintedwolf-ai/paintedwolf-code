package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverOMLXModelsConversationOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"id": "Qwen3-Coder-Next-8bit", "model_type": "llm"},
				{"id": "qwen3-vl-dir", "model_alias": "qwen3-vl", "model_type": "vlm"},
				{"id": "bge-m3", "model_type": "embedding"},
				{"id": "rerank-x", "model_type": "reranker"},
			},
		})
	}))
	defer srv.Close()

	models, err := OMLXModels(context.Background(), srv.URL+"/v1", "", srv.Client())
	testutil.FailErr(t, "discover omlx models", err)
	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2 conversation entries", models)
	}
	if models[0].ID != "Qwen3-Coder-Next-8bit" || models[1].ID != "qwen3-vl" {
		t.Fatalf("models = %+v", models)
	}
}

func TestDiscoverOMLXModelsRequiresBaseURL(t *testing.T) {
	_, err := OMLXModels(context.Background(), "", "", nil)
	if err == nil {
		t.Fatal("expected base_url error")
	}
}

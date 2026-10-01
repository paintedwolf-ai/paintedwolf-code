package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverGeminiModelsChatUsableOnly(t *testing.T) {
	const wantAuth = "gemini_test_key"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-goog-api-key") != wantAuth {
			http.Error(w, "missing goog key", http.StatusUnauthorized)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("native models.list must not send Authorization (got %q)", auth)
		}
		if got := r.URL.Query().Get("pageSize"); got != "1000" {
			t.Fatalf("pageSize = %q", got)
		}
		calls++
		if calls == 1 {
			if tok := r.URL.Query().Get("pageToken"); tok != "" {
				t.Fatalf("unexpected pageToken %q on first page", tok)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name": "models/gemini-3.5-flash",
						"supportedGenerationMethods": []string{
							"generateContent", "countTokens", "createCachedContent", "batchGenerateContent",
						},
						"inputTokenLimit": 1048576,
					},
					{
						"name":                       "models/embedding-001",
						"supportedGenerationMethods": []string{"embedContent"},
					},
				},
				"nextPageToken": "page-2",
			})
			return
		}
		if got := r.URL.Query().Get("pageToken"); got != "page-2" {
			t.Fatalf("pageToken = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					// Chat-capable but no createCachedContent: dropped, since
					// Interactions agents claim generateContent with this same
					// method set.
					"name":                       "models/gemma-4-31b-it",
					"supportedGenerationMethods": []string{"generateContent", "countTokens"},
				},
				{
					"name": "models/antigravity-preview-05-2026",
					// Live API falsely lists generateContent for this agent;
					// chat/completions rejects it. No createCachedContent.
					"supportedGenerationMethods": []string{"generateContent", "countTokens"},
				},
				{
					"name":                       "models/deep-research-preview-04-2026",
					"supportedGenerationMethods": []string{"generateContent", "countTokens"},
				},
				{
					"name":                       "models/gemini-2.5-flash",
					"supportedGenerationMethods": []string{"generateContent", "createCachedContent"},
				},
			},
		})
	}))
	defer srv.Close()

	models, err := GeminiModels(
		context.Background(),
		srv.URL+"/v1beta/openai",
		wantAuth,
		srv.Client(),
	)
	testutil.FailErr(t, "discover gemini models", err)
	if calls != 2 {
		t.Fatalf("pages fetched = %d, want 2", calls)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want generateContent+createCachedContent only", models)
	}
	if models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("first = %+v", models[0])
	}
	if models[1].ID != "gemini-3.5-flash" || models[1].ContextLength != 1048576 {
		t.Fatalf("second = %+v", models[1])
	}
}

func TestGeminiNativeBaseURL(t *testing.T) {
	got, err := geminiNativeBaseURL("https://generativelanguage.googleapis.com/v1beta/openai/")
	testutil.FailErr(t, "native base", err)
	if got != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatalf("base = %q", got)
	}
	if _, err := geminiNativeBaseURL("https://example.com/v1"); err == nil {
		t.Fatal("expected error for base_url without /openai")
	}
}

func TestDiscoverVertexExpressModelsValidatesKeyAndExcludesAgents(t *testing.T) {
	const validKey = "valid-express-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-goog-api-key") != validKey {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{
				"name":                       "models/gemini-2.5-flash",
				"supportedGenerationMethods": []string{"generateContent", "createCachedContent"},
			},
			{
				"name":                       "models/deep-research-preview-04-2026",
				"supportedGenerationMethods": []string{"generateContent", "countTokens"},
			},
		}})
	}))
	t.Cleanup(srv.Close)

	baseURL := srv.URL + "/v1beta/openai"
	models, err := GeminiModels(context.Background(), baseURL, validKey, srv.Client())
	testutil.FailErr(t, "discover Vertex Express models", err)
	if len(models) != 1 || models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("models = %+v, want the standard generateContent model only", models)
	}
	if _, err := GeminiModels(context.Background(), baseURL, "bogus", srv.Client()); err == nil {
		t.Fatal("bogus Vertex Express key passed discovery")
	}
}

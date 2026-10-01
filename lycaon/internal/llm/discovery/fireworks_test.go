package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverFireworksServerlessModels(t *testing.T) {
	const wantAuth = "fw_test_key"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/fireworks/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.URL.Query().Get("filter"); got != "supports_serverless=true" {
			t.Fatalf("filter = %q", got)
		}
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":               "accounts/fireworks/models/kimi-k2p6",
						"conversationConfig": map[string]string{"style": "default"},
					},
					{
						// Embedding models do not support chat completions.
						"name": "accounts/fireworks/models/nomic-embed-text-v1p5",
						"kind": "EMBEDDING_MODEL",
					},
				},
				"nextPageToken": "page-2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"name":               "accounts/fireworks/models/deepseek-v3p1",
					"conversationConfig": map[string]string{"style": "default"},
				},
				{
					// Rows without conversationConfig do not support chat completions.
					"name": "accounts/fireworks/models/llama-v3p1-8b-base",
				},
			},
		})
	}))
	defer srv.Close()

	ids, err := FireworksModels(
		context.Background(),
		srv.URL,
		wantAuth,
		srv.Client(),
	)
	testutil.FailErr(t, "discover fireworks models", err)
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want 2 chat-capable models (drop embedding + no conversationConfig)", ids)
	}
	if ids[0] != "accounts/fireworks/models/deepseek-v3p1" ||
		ids[1] != "accounts/fireworks/models/kimi-k2p6" {
		t.Fatalf("sorted ids = %v", ids)
	}
}

func TestFireworksDiscoveryOriginFollowsBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"stock catalog entry", "https://api.fireworks.ai/inference/v1", "https://api.fireworks.ai"},
		{"unset falls back", "", defaultFireworksDiscoveryBaseURL},
		{"unparseable falls back", "://nope", defaultFireworksDiscoveryBaseURL},
		{"local mirror", "http://127.0.0.1:8123/v1", "http://127.0.0.1:8123"},
		{"corporate gateway", "https://gw.corp.example/fw/v1", "https://gw.corp.example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fireworksDiscoveryOrigin(c.baseURL); got != c.want {
				t.Fatalf("origin(%q) = %q want %q", c.baseURL, got, c.want)
			}
		})
	}
}

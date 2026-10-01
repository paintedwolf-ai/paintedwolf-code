package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverCloudflareWorkersAIModels(t *testing.T) {
	const (
		wantAuth      = "cf_test_key"
		wantAccountID = "acct-123"
	)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/ai/models/search") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, wantAccountID) {
			http.Error(w, "bad account", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("task"); got != cloudflareDiscoverTaskTextGen {
			t.Fatalf("task = %q", got)
		}
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]string{
					{"name": "@cf/meta/llama-3.1-8b-instruct-fp8-fast"},
				},
				"result_info": map[string]any{"page": 1, "total_pages": 2},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result": []map[string]string{
				{"name": "@cf/qwen/qwen2.5-coder-32b-instruct"},
			},
			"result_info": map[string]any{"page": 2, "total_pages": 2},
		})
	}))
	defer srv.Close()

	prev := CloudflareAPIBaseURL
	CloudflareAPIBaseURL = srv.URL
	t.Cleanup(func() { CloudflareAPIBaseURL = prev })

	ids, err := CloudflareModels(
		context.Background(),
		wantAccountID,
		wantAuth,
		srv.Client(),
	)
	testutil.FailErr(t, "discover cloudflare models", err)
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	if ids[0] != "@cf/meta/llama-3.1-8b-instruct-fp8-fast" ||
		ids[1] != "@cf/qwen/qwen2.5-coder-32b-instruct" {
		t.Fatalf("sorted ids = %v", ids)
	}
}

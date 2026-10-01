package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Each live listing sends exactly one credential style.
func TestDiscoveryAuthContracts(t *testing.T) {
	const wantKey = "discovery-secret"

	t.Run("openai_compat_bearer_only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+wantKey {
				http.Error(w, "want bearer", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("x-api-key") != "" || r.Header.Get("api-key") != "" || r.Header.Get("x-goog-api-key") != "" {
				t.Fatalf("openai-compat must not send vendor key headers: %v", r.Header)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
		}))
		t.Cleanup(srv.Close)
		ids, err := discovery.OpenAIModels(context.Background(), srv.URL, wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverOpenAIModels", err)
		if len(ids) != 1 || ids[0] != "m1" {
			t.Fatalf("ids = %v", ids)
		}
	})

	t.Run("gemini_goog_key_no_bearer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-goog-api-key") != wantKey {
				http.Error(w, "want goog key", http.StatusUnauthorized)
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Fatalf("gemini native must not send Authorization (got %q)", auth)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{
					"name": "models/gemini-2.5-flash",
					"supportedGenerationMethods": []string{
						"generateContent", "createCachedContent",
					},
				}},
			})
		}))
		t.Cleanup(srv.Close)
		models, err := discovery.GeminiModels(context.Background(), srv.URL+"/v1beta/openai", wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverGeminiModels", err)
		if len(models) != 1 || models[0].ID != "gemini-2.5-flash" {
			t.Fatalf("models = %+v", models)
		}
	})

	t.Run("anthropic_x_api_key_no_bearer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != wantKey {
				http.Error(w, "want x-api-key", http.StatusUnauthorized)
				return
			}
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Fatalf("anthropic discovery must not send Authorization (got %q)", auth)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "claude-sonnet-4-6"}},
			})
		}))
		t.Cleanup(srv.Close)
		models, err := discovery.AnthropicModels(context.Background(), srv.URL+"/v1", wantKey, srv.Client())
		testutil.FailErr(t, "DiscoverAnthropicModels", err)
		if len(models) != 1 || models[0].ID != "claude-sonnet-4-6" {
			t.Fatalf("models = %+v", models)
		}
	})

	t.Run("azure_arm_bearer_only_never_data_plane_api_key", func(t *testing.T) {
		// Azure discovery reads Resource Manager with an AAD bearer token. It
		// never sends the data-plane api-key passed as wantKey and never calls
		// the data-plane host.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "arm-token"})
			case strings.Contains(r.URL.Path, "/deployments"):
				if r.Header.Get("api-key") != "" {
					t.Fatalf("ARM must not receive the data-plane api-key header")
				}
				if r.Header.Get("Authorization") != "Bearer arm-token" {
					http.Error(w, "want arm bearer", http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"value": []map[string]string{{"name": "gpt-4o-deploy"}},
				})
			default:
				t.Fatalf("unexpected ARM request path %q", r.URL.Path)
			}
		}))
		t.Cleanup(srv.Close)

		t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-1")
		t.Setenv("AZURE_RESOURCE_GROUP", "rg-1")
		t.Setenv("AZURE_TENANT_ID", "tenant-1")
		t.Setenv("AZURE_CLIENT_ID", "client-1")
		t.Setenv("AZURE_CLIENT_SECRET", "secret-1")
		t.Setenv("AZURE_AUTHORITY_HOST", srv.URL)
		restoreARMHost := discovery.AzureManagementHost
		discovery.AzureManagementHost = srv.URL
		t.Cleanup(func() { discovery.AzureManagementHost = restoreARMHost })

		models, err := discovery.AzureModels(context.Background(), "https://acme.openai.azure.com", srv.Client())
		testutil.FailErr(t, "DiscoverAzureModels", err)
		if len(models) != 1 || models[0].ID != "gpt-4o-deploy" {
			t.Fatalf("models = %+v", models)
		}
	})
}

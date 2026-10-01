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

func TestProbeAzureDataPlaneValidatesAPIKey(t *testing.T) {
	const validKey = "valid-azure-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("api-key") != validKey {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	t.Cleanup(srv.Close)

	testutil.FailErr(t, "probe valid Azure key", ProbeAzureDataPlane(t.Context(), srv.URL, validKey, srv.Client()))
	if err := ProbeAzureDataPlane(t.Context(), srv.URL, "bogus", srv.Client()); err == nil {
		t.Fatal("bogus Azure key passed the data-plane probe")
	}
}

func TestAzureAccountNameFromBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://acme.openai.azure.com":     "acme",
		"https://acme.openai.azure.com/":    "acme",
		"https://acme.openai.azure.com:443": "acme",
		"https://api.openai.com/v1":         "",
		"not a url":                         "",
		"":                                  "",
	}
	for baseURL, want := range cases {
		got, ok := azureAccountNameFromBaseURL(baseURL)
		if want == "" {
			if ok {
				t.Errorf("azureAccountNameFromBaseURL(%q) = %q, want unresolved", baseURL, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("azureAccountNameFromBaseURL(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestDiscoverAzureModelsEmptyWithoutARMConfig(t *testing.T) {
	t.Setenv("AZURE_SUBSCRIPTION_ID", "")
	t.Setenv("AZURE_RESOURCE_GROUP", "")

	models, err := AzureModels(t.Context(), "https://acme.openai.azure.com", nil)
	testutil.FailErr(t, "discover azure models", err)
	if models != nil {
		t.Fatalf("models = %+v, want nil when ARM subscription/resource group are unconfigured", models)
	}
}

func TestDiscoverAzureModelsEmptyWhenBaseURLIsNotAzureHost(t *testing.T) {
	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-1")
	t.Setenv("AZURE_RESOURCE_GROUP", "rg-1")

	models, err := AzureModels(t.Context(), "https://api.openai.com/v1", nil)
	testutil.FailErr(t, "discover azure models", err)
	if models != nil {
		t.Fatalf("models = %+v, want nil for a non-Azure base_url", models)
	}
}

// azureARMTestServer fakes both the AAD client-credentials token endpoint and
// the ARM deployments list endpoint behind one httptest server, wired via
// AZURE_AUTHORITY_HOST and AzureManagementHost.
func azureARMTestServer(t *testing.T, deploymentsStatus int, deploymentsBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			if r.Method != http.MethodPost {
				t.Fatalf("token request method = %s, want POST", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "arm-token", "expires_in": 3600})
		case strings.Contains(r.URL.Path, "/deployments"):
			if r.Header.Get("Authorization") != "Bearer arm-token" {
				http.Error(w, "missing arm bearer", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(deploymentsStatus)
			if deploymentsBody != "" {
				_, _ = w.Write([]byte(deploymentsBody))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverAzureModelsListsARMDeployments(t *testing.T) {
	srv := azureARMTestServer(t, http.StatusOK, `{"value":[
		{"name":"gpt-4o-prod","properties":{"model":{"name":"gpt-4o"}}},
		{"name":"gpt-4o-mini-dev","properties":{"model":{"name":"gpt-4o-mini"}}}
	]}`)

	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-1")
	t.Setenv("AZURE_RESOURCE_GROUP", "rg-1")
	t.Setenv("AZURE_TENANT_ID", "tenant-1")
	t.Setenv("AZURE_CLIENT_ID", "client-1")
	t.Setenv("AZURE_CLIENT_SECRET", "secret-1")
	t.Setenv("AZURE_AUTHORITY_HOST", srv.URL)

	restore := AzureManagementHost
	AzureManagementHost = srv.URL
	t.Cleanup(func() { AzureManagementHost = restore })

	models, err := AzureModels(t.Context(), "https://acme.openai.azure.com", srv.Client())
	testutil.FailErr(t, "discover azure models", err)
	if len(models) != 2 || models[0].ID != "gpt-4o-mini-dev" || models[1].ID != "gpt-4o-prod" {
		t.Fatalf("models = %+v, want the two ARM deployment names sorted", models)
	}
	if models[0].PricedAs != "gpt-4o-mini" || models[1].PricedAs != "gpt-4o" {
		t.Fatalf("models = %+v, want ARM-reported underlying model as priced_as", models)
	}
}

func TestDiscoverAzureModelsEmptyOn404(t *testing.T) {
	srv := azureARMTestServer(t, http.StatusNotFound, "")

	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-1")
	t.Setenv("AZURE_RESOURCE_GROUP", "rg-1")
	t.Setenv("AZURE_TENANT_ID", "tenant-1")
	t.Setenv("AZURE_CLIENT_ID", "client-1")
	t.Setenv("AZURE_CLIENT_SECRET", "secret-1")
	t.Setenv("AZURE_AUTHORITY_HOST", srv.URL)

	restore := AzureManagementHost
	AzureManagementHost = srv.URL
	t.Cleanup(func() { AzureManagementHost = restore })

	models, err := AzureModels(t.Context(), "https://acme.openai.azure.com", srv.Client())
	testutil.FailErr(t, "discover azure models", err)
	if models != nil {
		t.Fatalf("models = %+v, want nil on ARM 404 (no silent catalog ghost)", models)
	}
}

func TestAzureClientCredentialsTokenUnconfiguredReturnsEmpty(t *testing.T) {
	t.Setenv("AZURE_TENANT_ID", "")
	t.Setenv("AZURE_CLIENT_ID", "")
	t.Setenv("AZURE_CLIENT_SECRET", "")
	token, err := azureClientCredentialsToken(context.Background(), http.DefaultClient)
	testutil.FailErr(t, "client credentials token", err)
	if token != "" {
		t.Fatalf("token = %q, want empty when unconfigured", token)
	}
}

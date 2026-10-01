package api

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProviderPatchNullRestoresDefaultsAndOmissionPreservesValues(t *testing.T) {
	discovery := newProvADiscoveryServer(t)
	ship := strings.Replace(fakeProvidersYAML(discovery.URL), `api_key_env: ""`, `api_key_env: TEMPLATE_KEY`, 1)
	server, base := newProviderTestServerWithCatalogs(t, ship, ship)
	createProviderJSON(t, base, `{"id":"custom-instance","kind":"prov-a","label":"Custom","base_url":"https://proxy.example/v1","api_key_env":"CUSTOM_KEY","requires_api_key":false,"secret_screen_trusted":true,"models":[{"id":"custom-model"}]}`)
	cases := []struct {
		field string
		check func(t *testing.T)
	}{
		{"label", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if got.Label != "custom-instance" {
				t.Fatalf("default label=%q", got.Label)
			}
		}},
		{"api_key_env", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if got.APIKeyEnv != "TEMPLATE_KEY" {
				t.Fatalf("default key hint=%q", got.APIKeyEnv)
			}
		}},
		{"requires_api_key", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if !got.RequiresAPIKey || got.RequiresAPIKeyOverride {
				t.Fatal("kind authentication requirement was not restored")
			}
		}},
		{"secret_screen_trusted", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if got.SecretScreenTrust != "" {
				t.Fatal("trust default must require a human decision")
			}
		}},
		{"models", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if len(got.Models) != 0 {
				t.Fatalf("configured models=%+v", got.Models)
			}
		}},
		{"base_url", func(t *testing.T) {
			got, _ := server.llmSvc.Catalog.Get("custom-instance")
			if got.BaseURL != discovery.URL {
				t.Fatalf("default endpoint=%q, want %q", got.BaseURL, discovery.URL)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			putProviderJSON(t, base, "custom-instance", `{"`+tc.field+`":null}`)
			tc.check(t)
			before, ok := server.llmSvc.Catalog.Get("custom-instance")
			if !ok {
				t.Fatal("provider missing")
			}
			putProviderJSON(t, base, "custom-instance", `{}`)
			after, ok := server.llmSvc.Catalog.Get("custom-instance")
			if !ok {
				t.Fatal("provider missing after empty patch")
			}
			// The resolved state survives catalog reload as well as the HTTP projection.
			testutil.FailErr(t, "reload provider catalog", server.llmSvc.Catalog.Reload())
			tc.check(t)
			if before.Label != after.Label || before.BaseURL != after.BaseURL || before.APIKeyEnv != after.APIKeyEnv || before.RequiresAPIKey != after.RequiresAPIKey || before.SecretScreenTrust != after.SecretScreenTrust || len(before.Models) != len(after.Models) {
				t.Fatalf("empty patch changed provider: before=%+v after=%+v", before, after)
			}
		})
	}
}

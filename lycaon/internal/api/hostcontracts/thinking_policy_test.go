package hostcontracts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestThinkingOnlyPolicyPatchRoundTrip(t *testing.T) {
	discovery := contractfixture.NewProvADiscoveryServer(t)
	catalog := strings.Replace(contractfixture.FakeProvidersYAML(discovery.URL), "- id: model-x", "- id: model-x\n        thinking: {state: supported, efforts: [low, high, max]}", 1)
	_, base := contractfixture.NewProviderTestServerWithCatalogs(t, catalog, catalog)
	for _, body := range []string{
		`{"thinking_overrides":[{"provider_id":"prov-a","model":"model-x","mode":"fixed","effort":"high"}]}`,
		`{"thinking_overrides":[]}`,
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/model-policy", strings.NewReader(body))
		testutil.FailErr(t, "create thinking patch", err)
		req.Header.Set("Content-Type", "application/json")
		hostapi.WithTestAuth(req)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "save thinking patch", err)
		if resp.StatusCode != http.StatusOK {
			detail := contractfixture.ReadBody(t, resp)
			resp.Body.Close()
			t.Fatalf("thinking-only patch status=%d: %s", resp.StatusCode, detail)
		}
		var policy wire.ModelPolicy
		err = json.NewDecoder(resp.Body).Decode(&policy)
		resp.Body.Close()
		testutil.FailErr(t, "decode saved thinking policy", err)
		var expected struct {
			ThinkingOverrides []wire.ThinkingOverride `json:"thinking_overrides"`
		}
		testutil.FailErr(t, "decode expected patch", json.Unmarshal([]byte(body), &expected))
		if len(policy.ThinkingOverrides) != len(expected.ThinkingOverrides) {
			t.Fatalf("thinking-only patch lost: %+v", policy)
		}
		if len(policy.ThinkingOverrides) > 0 && policy.ThinkingOverrides[0].Effort != "high" {
			t.Fatal("native effort changed during API round trip")
		}
	}
}

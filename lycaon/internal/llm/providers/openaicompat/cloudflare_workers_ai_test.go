package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFetchCloudflareDailyNeurons(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"accounts": []map[string]any{
						{
							"aiInferenceAdaptiveGroups": []map[string]any{
								{"sum": map[string]any{"totalNeurons": 2500}},
								{"sum": map[string]any{"totalNeurons": 1500}},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	prev := cloudflareGraphQLURL
	cloudflareGraphQLURL = srv.URL + "/graphql"
	t.Cleanup(func() { cloudflareGraphQLURL = prev })

	used, err := fetchCloudflareDailyNeurons(context.Background(), srv.Client(), "acct-1", "tok")
	testutil.FailErr(t, "fetch daily neurons", err)
	if used != 4000 {
		t.Fatalf("used = %v, want 4000", used)
	}
}

func TestCloudflareAccountIDFromBaseURL(t *testing.T) {
	id, err := CloudflareAccountIDFromBaseURL(
		"https://api.cloudflare.com/client/v4/accounts/abc123def/ai/v1",
	)
	testutil.FailErr(t, "CloudflareAccountIDFromBaseURL failed", err)
	if id != "abc123def" {
		t.Fatalf("id = %q", id)
	}
	if _, err := CloudflareAccountIDFromBaseURL(
		"https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1",
	); err == nil {
		t.Fatal("expected placeholder rejection")
	}
}

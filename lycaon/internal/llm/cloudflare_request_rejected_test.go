package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/pkg/api"
	openai "github.com/sashabaranov/go-openai"
)

func TestCloudflarePaidPlanRejectionCompleteAndStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":5035,"message":"private provider diagnostic"}],"success":false}`))
	}))
	defer server.Close()
	entry := CatalogEntry{ID: "cloudflare-workers-ai-1", BaseURL: server.URL + "/accounts/account-1/ai/v1"}
	entry.RejectionReasons = bundledCloudflareRejectionRules(t)
	inner := openaicompat.NewCloudflare(entry.ID, entry.BaseURL, entry.Models, "test-token", nil, server.Client())
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Entries: map[string]CatalogEntry{entry.ID: entry}, Providers: map[string]modelcall.Provider{entry.ID: inner},
	})
	provider := (&Registry{}).decorateProvider(snapshot, inner)
	request := modelcall.CompletionRequest{
		Model:    "@cf/zai-org/glm-5.3-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}
	for _, stream := range []bool{false, true} {
		var err error
		if stream {
			_, err = provider.Stream(t.Context(), request)
		} else {
			_, err = provider.Complete(t.Context(), request)
		}
		rejected, ok := providerretry.AsProviderRequestRejected(err)
		if !ok || rejected.Reason != providerretry.RejectionCloudflareWorkersPaidRequired ||
			rejected.ProviderID != entry.ID || rejected.Model != request.Model {
			t.Fatalf("stream=%v: missing paid plan rejection: %v", stream, err)
		}
		var response *openai.RequestError
		if !errors.As(err, &response) {
			t.Fatal("original provider response was not preserved for diagnostics")
		}
	}
}

func TestCloudflareRejectionRequiresStructuredCode(t *testing.T) {
	rules := bundledCloudflareRejectionRules(t)
	for _, body := range []string{
		`{"errors":[{"code":10000,"message":"Workers Paid plan 5035"}]}`,
		`{"errors":[{"message":"Workers Paid plan 5035"}]}`,
		`{"errors":[{"code":5035},{"code":10000}]}`,
		`{"errors":[]}`,
		`Workers Paid plan 5035`,
	} {
		_, err := providerretry.RunProviderAttempts(t.Context(), providerretry.ProviderAttempt{ //nolint:bodyclose // A failed attempt returns no response.
			ProviderID: "fixture",
			Send: func(context.Context) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			},
		})
		rejected, ok := providerretry.AsProviderRequestRejected(err)
		if !ok {
			t.Fatalf("403 did not produce a request rejection: %v", err)
		}
		if got := rejectionReason(err, rules); !errors.Is(got, err) || rejected.Reason != "" {
			t.Fatalf("unclassified response changed: %s", body)
		}
	}
}

func bundledCloudflareRejectionRules(t *testing.T) []ProviderRejectionRule {
	t.Helper()
	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load provider reasons: %v", err)
	}
	ship := map[string]ProviderEntry{}
	for _, provider := range cfg.Providers {
		ship[provider.ID] = provider
	}
	entry, err := resolveLocalEntry(ProviderEntry{ID: "fixture", Kind: "cloudflare-workers-ai"}, ship)
	if err != nil || len(entry.RejectionReasons) != 1 {
		t.Fatalf("inherit Cloudflare reasons: %+v, %v", entry, err)
	}
	return entry.RejectionReasons
}

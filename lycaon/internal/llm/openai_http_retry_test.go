package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNewProviderForEntryAttachesHTTPRetry(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  7,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1, 1, 1, 1, 1, 1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
	for _, kind := range []string{"openai", "anthropic", "ollama", "fireworks"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Model discovery and context probes share the server; count completion attempts only.
				path := r.URL.Path
				if strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/api/chat") {
					calls.Add(1)
				}
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			}))
			defer server.Close()
			entry := CatalogEntry{ID: "p", Kind: kind, BaseURL: server.URL + "/v1", HTTPRetry: policy, Models: []modelinfo.Entry{{ID: "model", ContextLength: 32768, ThinkStyle: "none"}}}
			reg, err := NewRegistry(t.Context(), mustTestProviderCatalog(t, entry), providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			p, err := newProviderForEntry(context.Background(), entry, "key", reg.providerRetryPolicy(entry), reg.cloudflareUsage, reg.discoveryClient)
			if err != nil {
				t.Fatalf("newProviderForEntry: %v", err)
			}
			_, err = p.Complete(t.Context(), modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}})
			limited, ok := failure.AsProviderRateLimited(err)
			if !ok || limited.Attempts != 8 || calls.Load() != 8 {
				t.Fatalf("factory retry policy: requests=%d error=%v, want eight rate-limited attempts", calls.Load(), err)
			}
		})
	}

}

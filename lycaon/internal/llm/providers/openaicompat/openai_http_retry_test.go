package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/pkg/api"
)

func testRetryPolicy() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   50,
		BackoffMs:   []int{1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
}

func TestOpenAICompleteHTTPRetryThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer srv.Close()

	p := New("test", srv.URL, "key", nil).WithHTTPRetry(testRetryPolicy())
	out, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "m",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d want 2", hits.Load())
	}
}

func TestOpenAICompleteHTTPRetryExhausted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer srv.Close()

	p := New("test", srv.URL, "key", nil).WithHTTPRetry(testRetryPolicy())
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "m",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	rateLimited, ok := failure.AsProviderRateLimited(err)
	if !ok {
		t.Fatalf("err = %v want ProviderRateLimitedError", err)
	}
	if rateLimited.ProviderID != "test" || rateLimited.Model != "m" || rateLimited.Attempts != 3 {
		t.Fatalf("rate limited provenance = %+v", rateLimited)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d want 3 (1 + 2 retries)", hits.Load())
	}
}

func TestOpenAICompleteTransientServerErrorExhausted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"EngineCore encountered an issue. See stack trace above."}}`))
	}))
	defer srv.Close()

	policy := testRetryPolicy()
	policy.Statuses = []int{429, 500, 502, 504}
	p := New("together-ai-1", srv.URL, "key", nil).WithHTTPRetry(policy)
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "zai-org/GLM-5.3-Flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	server, ok := failure.AsProviderServer(err)
	if !ok {
		t.Fatalf("err = %v want ProviderServerError", err)
	}
	if server.ProviderID != "together-ai-1" || server.Model != "zai-org/GLM-5.3-Flash" ||
		server.Status != 500 || server.Attempts != 3 {
		t.Fatalf("server error provenance = %+v", server)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d want 3 (1 + 2 retries)", hits.Load())
	}
}

func TestOpenAICompleteNoRetryOn400(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	p := New("test", srv.URL, "key", []modelinfo.Entry{{ID: "m", ThinkStyle: "none"}}).WithHTTPRetry(providerretry.ProviderHTTPRetry{
		MaxRetries:  3,
		MaxWaitMs:   50,
		BackoffMs:   []int{1, 1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "m",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := failure.AsProviderRateLimited(err); ok {
		t.Fatal("400 must not map to rate limited")
	}
	rejected, ok := providerretry.AsProviderRequestRejected(err)
	if !ok || rejected.Status != 400 || rejected.ProviderID != "test" || rejected.Model != "m" ||
		rejected.NoticeCode() != "provider_request_rejected" {
		t.Fatalf("request rejection lost its identity: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d want 1", hits.Load())
	}
}

func TestOpenAICompleteHTTPRetryExhaustedOverloaded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"engine overloaded"}}`))
	}))
	defer srv.Close()

	p := New("test", srv.URL, "key", nil).WithHTTPRetry(providerretry.ProviderHTTPRetry{
		MaxRetries:  1,
		MaxWaitMs:   50,
		BackoffMs:   []int{1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
		Capacity: &providerretry.ProviderHTTPRetry{
			MaxRetries:  1,
			MaxWaitMs:   50,
			BackoffMs:   []int{1},
			Statuses:    []int{503},
			WaitHeaders: []string{"Retry-After"},
		},
	})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "m",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if _, ok := failure.AsProviderOverloaded(err); !ok {
		t.Fatalf("err = %v want ProviderOverloadedError", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d want 2 (1 + 1 capacity retry)", hits.Load())
	}
}

func TestOpenAICompleteMaxRetriesZero(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer srv.Close()

	p := New("test", srv.URL, "key", nil).WithHTTPRetry(providerretry.ProviderHTTPRetry{
		MaxRetries:  0,
		MaxWaitMs:   50,
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	})
	_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Model:    "m",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if _, ok := failure.AsProviderRateLimited(err); !ok {
		t.Fatalf("err = %v want ProviderRateLimitedError", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d want 1", hits.Load())
	}
}

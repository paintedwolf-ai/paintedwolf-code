package providerretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

func TestExhaustedStatusFailureCarriesDeclaredRetryAfter(t *testing.T) {
	policy := ProviderHTTPRetry{WaitHeaders: []string{"Retry-After"}}
	for _, status := range []int{429, 503, 500} {
		attempt := ProviderAttempt{ProviderID: "fixture", Model: "m", Policy: policy, Send: func(context.Context) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"300"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}}
		response, err := RunProviderAttempts(t.Context(), attempt)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil {
			t.Fatalf("status %d produced no failure", status)
		}
		if got := failure.DeclaredRetryAfter(err); got != 300*time.Second {
			t.Fatalf("status %d: retry after %s from %v", status, got, err)
		}
	}
	if got := failure.DeclaredRetryAfter(errors.New("untyped")); got != 0 {
		t.Fatalf("untyped failure declared %s", got)
	}
}

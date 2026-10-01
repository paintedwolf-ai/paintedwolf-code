package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// The transport bounds the response-header wait.
func TestStreamingHTTPClientResponseHeaderTimeout(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the connection past the header deadline.
		time.Sleep(500 * time.Millisecond)
		fmt.Fprintln(w, "too late")
	}))
	defer mockServer.Close()

	client := providerhttp.NewStreamingClient(50 * time.Millisecond)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, mockServer.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("expected header-timeout error, got response in %v", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("header timeout did not fire: elapsed %v (want < 300ms)", elapsed)
	}
}

// The header timeout does not limit the response body.
func TestStreamingHTTPClientAllowsLongStreamBody(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		for i := 0; i < 5; i++ {
			fmt.Fprint(w, "data: chunk\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(40 * time.Millisecond)
		}
		fmt.Fprintln(w, "data: [DONE]")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer mockServer.Close()

	client := providerhttp.NewStreamingClient(50 * time.Millisecond)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, mockServer.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
}

// Non-streaming generation needs a longer header wait.
func TestCompleteHTTPClientUsesLongerHeaderTimeout(t *testing.T) {
	if httpclient.DefaultResponseHeader != time.Minute {
		t.Fatalf("remote stream ResponseHeaderTimeout = %v, want 1m", httpclient.DefaultResponseHeader)
	}
	p := New("openai", "https://api.openai.com/v1", "k", nil)
	streamTO := responseHeaderTimeoutOf(p.streamClient)
	completeTO := responseHeaderTimeoutOf(p.completeClient)
	if streamTO != httpclient.DefaultResponseHeader {
		t.Fatalf("stream ResponseHeaderTimeout = %v, want %v", streamTO, httpclient.DefaultResponseHeader)
	}
	if completeTO != providerprofile.HostedCompleteResponseHeaderTimeout {
		t.Fatalf("complete ResponseHeaderTimeout = %v, want %v", completeTO, providerprofile.HostedCompleteResponseHeaderTimeout)
	}
	if completeTO <= streamTO {
		t.Fatalf("complete timeout %v must exceed stream TTFT %v", completeTO, streamTO)
	}
}

func TestLocalOpenAICompatibleCompleteOutlivesBackgroundBudget(t *testing.T) {
	p := New("local", "http://localhost:1234/v1", "", nil).
		WithProfile(providerprofile.Lmstudio())
	got := responseHeaderTimeoutOf(p.completeClient)
	if got != providerprofile.LocalInferenceCompleteResponseHeaderTimeout {
		t.Fatalf("complete ResponseHeaderTimeout = %v want %v", got, providerprofile.LocalInferenceCompleteResponseHeaderTimeout)
	}
	if got <= p.Profile().BackgroundUtilityCallTimeout {
		t.Fatalf("transport timeout %v must outlive background context %v", got, p.Profile().BackgroundUtilityCallTimeout)
	}
}

func responseHeaderTimeoutOf(client *http.Client) time.Duration {
	if client == nil || client.Transport == nil {
		return 0
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		return 0
	}
	return tr.ResponseHeaderTimeout
}

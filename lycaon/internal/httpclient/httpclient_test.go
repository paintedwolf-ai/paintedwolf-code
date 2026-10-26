package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
)

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func TestBoundedTimesOutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	_, err := Bounded(egressclass.ProviderModelDiscovery, 200*time.Millisecond).Get(srv.URL) //nolint:bodyclose // the request is expected to fail
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !isTimeout(err) {
		t.Fatalf("error is not a timeout: %v", err)
	}
}

func TestBoundedTimeoutCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk "))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	resp, err := Bounded(egressclass.ProviderModelDiscovery, 500*time.Millisecond).Get(srv.URL)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if _, err = io.ReadAll(resp.Body); err == nil {
			t.Fatal("Bounded must fail on a body that outlives its total timeout — this is why streams need Streaming")
		}
	}
}

func TestStreamingSurvivesSlowBody(t *testing.T) {
	const chunks = 20

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < chunks; i++ {
			time.Sleep(50 * time.Millisecond)
			if r.Context().Err() != nil {
				return
			}
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	resp, err := Streaming(egressclass.LLMProviderRequest).Get(srv.URL)
	if err != nil {
		testutil.FailErr(t, "streaming get", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		testutil.FailErr(t, "read streamed body", err)
	}
	if len(body) != chunks {
		t.Fatalf("read %d bytes, want %d — a legitimate slow completion was truncated", len(body), chunks)
	}
}

func TestStreamingBoundsResponseHeaderWait(t *testing.T) {
	orig := DefaultResponseHeader
	DefaultResponseHeader = 150 * time.Millisecond
	defer func() { DefaultResponseHeader = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	_, err := Streaming(egressclass.LLMProviderRequest).Get(srv.URL) //nolint:bodyclose // the request is expected to fail
	if err == nil || !isTimeout(err) {
		t.Fatalf("Streaming response-header bound error = %v, want timeout", err)
	}
}

func TestProfilesFailFastOnClosedPort(t *testing.T) {
	url := closedPortURL(t)

	for name, client := range map[string]*http.Client{
		"bounded":   Bounded(egressclass.ProviderModelDiscovery, 5*time.Second),
		"streaming": Streaming(egressclass.LLMProviderRequest),
		"download":  Download(egressclass.BrowserEngineDownload, time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Get(url); err == nil || isTimeout(err) { //nolint:bodyclose // the request is expected to fail
				t.Fatalf("closed port error = %v, want immediate connection error rather than timeout", err)
			}
		})
	}
}

func TestProfilesPreserveProxySupport(t *testing.T) {
	for name, client := range map[string]*http.Client{
		"bounded":   Bounded(egressclass.ProviderModelDiscovery, time.Second),
		"streaming": Streaming(egressclass.LLMProviderRequest),
		"download":  Download(egressclass.BrowserEngineDownload, time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			tr, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport is %T, want *http.Transport", client.Transport)
			}
			if tr.Proxy == nil {
				t.Fatal("transport has no Proxy func: users behind a corporate proxy would break")
			}
			if tr.TLSHandshakeTimeout != DefaultTLSHandshake {
				t.Fatalf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, DefaultTLSHandshake)
			}
			if tr == http.DefaultTransport {
				t.Fatal("profile shares http.DefaultTransport instead of cloning it")
			}
		})
	}
}

func TestStreamingHasNoTotalTimeout(t *testing.T) {
	if got := Streaming(egressclass.LLMProviderRequest).Timeout; got != 0 {
		t.Fatalf("Streaming timeout = %v, want 0 — a total timeout kills long completions mid-stream", got)
	}
}

func TestBoundedCarriesTotalTimeout(t *testing.T) {
	if got := Bounded(egressclass.ProviderModelDiscovery, 7*time.Second).Timeout; got != 7*time.Second {
		t.Fatalf("Bounded timeout = %v, want 7s", got)
	}
}

func TestWithTransportPreservesProfileTimeout(t *testing.T) {
	base := Bounded(egressclass.ProviderModelDiscovery, 3*time.Second)
	wrapped := WithTransport(base, http.NewFileTransport(http.Dir(t.TempDir())))

	if wrapped.Timeout != 3*time.Second {
		t.Fatalf("wrapped Timeout = %v, want 3s — WithTransport must keep the profile's bounds", wrapped.Timeout)
	}
	if base.Transport == wrapped.Transport {
		t.Fatal("WithTransport mutated the original client instead of returning a copy")
	}
}

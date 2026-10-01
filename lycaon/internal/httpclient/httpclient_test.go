package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	start := time.Now()
	_, err := Bounded(egressclass.ProviderModelDiscovery, 200*time.Millisecond).Get(srv.URL) //nolint:bodyclose // the request is expected to fail
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !isTimeout(err) {
		t.Fatalf("error is not a timeout: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("took %v to time out on a 200ms bound", elapsed)
	}
}

func TestBoundedTimeoutCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < 30; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			_, _ = w.Write([]byte("chunk "))
			w.(http.Flusher).Flush()
		}
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
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
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
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	start := time.Now()
	_, err := Streaming(egressclass.LLMProviderRequest).Get(srv.URL) //nolint:bodyclose // the request is expected to fail
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Streaming must still bound the wait for response headers")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took %v to give up on a 150ms response-header bound", elapsed)
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
			start := time.Now()
			if _, err := client.Get(url); err == nil { //nolint:bodyclose // the request is expected to fail
				t.Fatal("expected a connection error against a closed port")
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("took %v to fail against a closed port", elapsed)
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

func TestSystemProxyUsedWithoutEnvironmentOverride(t *testing.T) {
	clearProxyEnvironment(t)
	want, err := url.Parse("http://system-proxy.example:8080")
	testutil.FailErr(t, "parse proxy", err)
	previous := systemProxyLookup
	systemProxyLookup = func(*url.URL) (*url.URL, error) { return want, nil }
	t.Cleanup(func() { systemProxyLookup = previous })

	request, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	testutil.FailErr(t, "new request", err)
	transport := Bounded(egressclass.ProviderModelDiscovery, time.Second).Transport.(*http.Transport)
	got, err := transport.Proxy(request)
	testutil.FailErr(t, "resolve proxy", err)
	if got == nil || got.String() != want.String() {
		t.Fatalf("proxy = %v, want %v", got, want)
	}
}

func TestEnvironmentProxyOverridesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("HTTPS_PROXY", "http://environment-proxy.example:9090")
	called := false
	previous := systemProxyLookup
	systemProxyLookup = func(*url.URL) (*url.URL, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { systemProxyLookup = previous })

	request, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	testutil.FailErr(t, "new request", err)
	transport := Bounded(egressclass.ProviderModelDiscovery, time.Second).Transport.(*http.Transport)
	got, err := transport.Proxy(request)
	testutil.FailErr(t, "resolve proxy", err)
	if got == nil || got.String() != "http://environment-proxy.example:9090" {
		t.Fatalf("proxy = %v", got)
	}
	if called {
		t.Fatal("system proxy was consulted despite explicit HTTPS_PROXY")
	}
}

func TestNoProxyBypassesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("NO_PROXY", ".example.com")
	called := false
	previous := systemProxyLookup
	systemProxyLookup = func(*url.URL) (*url.URL, error) {
		called = true
		return url.Parse("http://system-proxy.example:8080")
	}
	t.Cleanup(func() { systemProxyLookup = previous })

	request, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	testutil.FailErr(t, "new request", err)
	transport := Bounded(egressclass.ProviderModelDiscovery, time.Second).Transport.(*http.Transport)
	proxy, err := transport.Proxy(request)
	testutil.FailErr(t, "resolve proxy", err)
	if proxy != nil {
		t.Fatalf("proxy = %v, want NO_PROXY bypass", proxy)
	}
	if called {
		t.Fatal("system proxy was consulted despite NO_PROXY match")
	}
}

func TestLoopbackNeverUsesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	called := false
	previous := systemProxyLookup
	systemProxyLookup = func(*url.URL) (*url.URL, error) {
		called = true
		return url.Parse("http://system-proxy.example:8080")
	}
	t.Cleanup(func() { systemProxyLookup = previous })

	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8787/health", nil)
	testutil.FailErr(t, "new request", err)
	transport := Bounded(egressclass.ProviderModelDiscovery, time.Second).Transport.(*http.Transport)
	proxy, err := transport.Proxy(request)
	testutil.FailErr(t, "resolve proxy", err)
	if proxy != nil {
		t.Fatalf("proxy = %v, want direct loopback", proxy)
	}
	if called {
		t.Fatal("system proxy was consulted for loopback")
	}
}

func clearProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
	} {
		t.Setenv(key, "")
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

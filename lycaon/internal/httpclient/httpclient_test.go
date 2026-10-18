package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/systemproxy"
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

func useSystemProxy(t *testing.T, raw string) {
	t.Helper()
	proxy, err := url.Parse(raw)
	testutil.FailErr(t, "parse proxy", err)
	previous := systemProxyConfiguration
	systemProxyConfiguration = func() (systemproxy.Configuration, error) {
		return systemproxy.Configuration{Source: systemproxy.SourceGSettings, Mode: systemproxy.ModeManual, HTTP: proxy, HTTPS: proxy}, nil
	}
	t.Cleanup(func() { systemProxyConfiguration = previous })
}

func transportProxy(t *testing.T, raw string) *url.URL {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, raw, nil)
	testutil.FailErr(t, "new request", err)
	transport := Bounded(egressclass.ProviderModelDiscovery, time.Second).Transport.(*http.Transport)
	got, err := transport.Proxy(request)
	testutil.FailErr(t, "resolve proxy", err)
	return got
}

func TestSystemProxyUsedWithoutEnvironmentOverride(t *testing.T) {
	clearProxyEnvironment(t)
	useSystemProxy(t, "http://system-proxy.example:8080")
	if got := transportProxy(t, "https://api.example.com/v1"); got == nil || got.String() != "http://system-proxy.example:8080" {
		t.Fatalf("proxy = %v", got)
	}
}

func TestEnvironmentProxyOverridesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("HTTPS_PROXY", "http://environment-proxy.example:9090")
	useSystemProxy(t, "http://system-proxy.example:8080")
	if got := transportProxy(t, "https://api.example.com/v1"); got == nil || got.String() != "http://environment-proxy.example:9090" {
		t.Fatalf("proxy = %v", got)
	}
	if got := transportProxy(t, "http://api.example.com/v1"); got == nil || got.String() != "http://system-proxy.example:8080" {
		t.Fatalf("http proxy = %v, want the system proxy for a scheme without an environment proxy", got)
	}
}

func TestNoProxyBypassesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("NO_PROXY", ".example.com")
	useSystemProxy(t, "http://system-proxy.example:8080")
	if got := transportProxy(t, "https://api.example.com/v1"); got != nil {
		t.Fatalf("proxy = %v, want NO_PROXY bypass", got)
	}
	if got := transportProxy(t, "https://example.com/v1"); got == nil {
		t.Fatal("a leading-dot NO_PROXY entry exempted the name itself")
	}
}

func TestLoopbackNeverUsesSystemProxy(t *testing.T) {
	clearProxyEnvironment(t)
	useSystemProxy(t, "http://system-proxy.example:8080")
	if got := transportProxy(t, "http://127.0.0.1:8787/health"); got != nil {
		t.Fatalf("proxy = %v, want direct loopback", got)
	}
}

func TestUnreadableSystemConfigurationFailsOnlyResolutionsThatNeedIt(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("HTTPS_PROXY", "proxy.example:3128")
	previous := systemProxyConfiguration
	systemProxyConfiguration = func() (systemproxy.Configuration, error) {
		return systemproxy.Configuration{}, errors.New("settings tool failed")
	}
	t.Cleanup(func() { systemProxyConfiguration = previous })
	config := CurrentProxyConfiguration()
	if got, err := config.ProxyFor(mustURL(t, "https://api.example/")); err != nil || got == nil || got.String() != "http://proxy.example:3128" {
		t.Fatalf("explicit environment proxy = %v, %v", got, err)
	}
	if _, err := config.ProxyFor(mustURL(t, "http://api.example/")); err == nil || !strings.Contains(err.Error(), "settings tool failed") {
		t.Fatalf("system-dependent resolution = %v", err)
	}
}

func TestEnvironmentExceptionGrammar(t *testing.T) {
	for entry, cases := range map[string]map[string]bool{
		"example.com":      {"example.com": true, "a.example.com": true, "notexample.com": false},
		".example.com":     {"example.com": false, "a.example.com": true},
		"*.example.com":    {"example.com": false, "a.example.com": true},
		"example.com:8443": {"example.com:8443": true, "example.com": false},
		"10.0.0.0/8":       {"10.2.3.4": true, "11.0.0.1": false},
		"192.0.2.7":        {"192.0.2.7": true, "192.0.2.8": false},
		"*":                {"anything.example": true},
	} {
		exception := parseEnvironmentException(entry)
		for target, want := range cases {
			host, port, err := net.SplitHostPort(target)
			if err != nil {
				host, port = target, "443"
			}
			if got := exception.Matches(host, port); got != want {
				t.Fatalf("NO_PROXY %q on %s = %t, want %t (%+v)", entry, target, got, want, exception)
			}
		}
	}
	if got, err := parseEnvironmentProxy("proxy.example:3128"); err != nil || got == nil || got.String() != "http://proxy.example:3128" {
		t.Fatalf("bare environment proxy = %v", got)
	}
	if got, err := parseEnvironmentProxy("socks5://socks.example:1080"); err != nil || got == nil || got.Scheme != "socks5" {
		t.Fatalf("socks environment proxy = %v", got)
	}
	t.Setenv("REQUEST_METHOD", "GET")
	t.Setenv("HTTP_PROXY", "http://cgi-injected.example:80")
	if CurrentProxyConfiguration().EnvironmentHTTP != nil {
		t.Fatal("HTTP_PROXY was honoured inside a CGI request")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	target, err := url.Parse(raw)
	testutil.FailErr(t, "parse url", err)
	return target
}

func clearProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD",
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

func TestMalformedEnvironmentProxyDoesNotFallBack(t *testing.T) {
	clearProxyEnvironment(t)
	const secret = "private-proxy-secret"
	for _, raw := range []string{"ftp://proxy.example:21", "http://alice:" + secret + "@proxy.example/%zz", "http://proxy.example/path", "http://proxy.example/?key=" + secret} {
		t.Run(raw[:4], func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", raw)
			t.Setenv("NO_PROXY", "direct.example")
			config := CurrentProxyConfiguration()
			_, err := config.ProxyFor(mustURL(t, "https://remote.example/"))
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("invalid environment proxy must fail without credentials: %v", err)
			}
			for _, target := range []string{"https://direct.example/", "https://localhost/"} {
				proxy, err := config.ProxyFor(mustURL(t, target))
				testutil.FailErr(t, "resolve exempt destination", err)
				if proxy != nil {
					t.Fatalf("exempt destination selected %v", proxy)
				}
			}
		})
	}
}

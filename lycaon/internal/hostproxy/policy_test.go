package hostproxy

import (
	"errors"
	"github.com/lycaon/lycaon/internal/systemproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

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
	got, err := ForRequest(request)
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
	config := Current()
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
	if Current().EnvironmentHTTP != nil {
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

func TestMalformedEnvironmentProxyDoesNotFallBack(t *testing.T) {
	clearProxyEnvironment(t)
	const secret = "private-proxy-secret"
	for _, raw := range []string{"ftp://proxy.example:21", "http://alice:" + secret + "@proxy.example/%zz", "http://proxy.example/path", "http://proxy.example/?key=" + secret} {
		t.Run(raw[:4], func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", raw)
			t.Setenv("NO_PROXY", "direct.example")
			config := Current()
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

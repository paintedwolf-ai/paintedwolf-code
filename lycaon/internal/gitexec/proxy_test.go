package gitexec

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/systemproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/net/http/httpproxy"
)

func proxyURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	testutil.FailErr(t, "parse proxy", err)
	return parsed
}

func desktop(t *testing.T, proxy string, exceptions ...string) systemproxy.Configuration {
	t.Helper()
	config := systemproxy.Configuration{Source: systemproxy.SourceGSettings, Mode: systemproxy.ModeManual}
	if proxy != "" {
		config.HTTP, config.HTTPS = proxyURL(t, proxy), proxyURL(t, proxy)
	}
	for _, entry := range exceptions {
		config.Exceptions = append(config.Exceptions, systemproxy.ParseDesktopException(entry))
	}
	return config
}

func environmentOf(env []string) map[string]string {
	values := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	return values
}

func TestPerHostPlanTranslatesTheWholePolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		config  httpclient.ProxyConfiguration
		http    string
		https   string
		noProxy string
	}{
		"desktop exceptions": {
			config: httpclient.ProxyConfiguration{System: desktop(t, "http://alice:pw@proxy.example:3128",
				"*.corp.example", ".internal.example", "build.example", "10.0.0.0/8", "192.0.2.7", "[2001:db8::1]", "169.254/16")},
			http: "http://alice:pw@proxy.example:3128", https: "http://alice:pw@proxy.example:3128",
			noProxy: "localhost,127.0.0.0/8,::1,corp.example,internal.example,build.example,10.0.0.0/8,192.0.2.7,2001:db8::1,169.254.0.0/16",
		},
		"environment exceptions apply to the desktop proxy": {
			config: httpclient.ProxyConfiguration{
				EnvironmentExceptions: []systemproxy.Exception{systemproxy.ParseDesktopException("mirror.example")},
				System:                desktop(t, "http://proxy.example:3128", "corp.example"),
			},
			http: "http://proxy.example:3128", https: "http://proxy.example:3128",
			noProxy: "localhost,127.0.0.0/8,::1,mirror.example,corp.example",
		},
		"every host excepted": {
			config: httpclient.ProxyConfiguration{System: desktop(t, "http://proxy.example:3128", "corp.example", "*")},
			http:   "http://proxy.example:3128", https: "http://proxy.example:3128", noProxy: "*",
		},
		"socks serves both schemes": {
			config: httpclient.ProxyConfiguration{System: systemproxy.Configuration{
				Source: systemproxy.SourceKIO, Mode: systemproxy.ModeManual, SOCKS: proxyURL(t, "socks5://socks.example:1080"),
			}},
			http: "socks5://socks.example:1080", https: "socks5://socks.example:1080", noProxy: "localhost,127.0.0.0/8,::1",
		},
		"environment proxies alone ignore the desktop exceptions": {
			config: httpclient.ProxyConfiguration{
				EnvironmentHTTP: proxyURL(t, "http://env.example:8080"), EnvironmentHTTPS: proxyURL(t, "http://env.example:8080"),
				System: desktop(t, "http://proxy.example:3128", "*.corp.example", "foo*.example"),
			},
			http: "http://env.example:8080", https: "http://env.example:8080", noProxy: "localhost,127.0.0.0/8,::1",
		},
		"no proxy configured": {
			config:  httpclient.ProxyConfiguration{System: systemproxy.Configuration{Source: systemproxy.SourceNone, Mode: systemproxy.ModeNone}},
			noProxy: "localhost,127.0.0.0/8,::1",
		},
	} {
		plan, err := planFor(tc.config, "https://git.example/r.git")
		testutil.FailErr(t, name, err)
		if plan.Mode != ProxyPlanPerHost || len(plan.Reasons) != 0 {
			t.Fatalf("%s: plan = %+v", name, plan)
		}
		env := environmentOf(plan.Environment())
		for variable, want := range map[string]string{
			"http_proxy": tc.http, "HTTP_PROXY": tc.http, "https_proxy": tc.https, "HTTPS_PROXY": tc.https,
			"all_proxy": "", "ALL_PROXY": "", "no_proxy": tc.noProxy, "NO_PROXY": tc.noProxy,
		} {
			if got, ok := env[variable]; !ok || got != want {
				t.Fatalf("%s: %s = %q, want %q", name, variable, got, want)
			}
		}
		assertGitGrammarAgrees(t, name, tc.config, env)
	}
}

// assertGitGrammarAgrees checks that the variables, read with Go's NO_PROXY
// grammar (the one Git LFS uses, and a subset curl reads identically), pick
// the same proxy as host clients for every destination.
func assertGitGrammarAgrees(t *testing.T, name string, config httpclient.ProxyConfiguration, env map[string]string) {
	t.Helper()
	git := (&httpproxy.Config{HTTPProxy: env["HTTP_PROXY"], HTTPSProxy: env["HTTPS_PROXY"], NoProxy: env["NO_PROXY"]}).ProxyFunc()
	for _, raw := range []string{
		"https://github.com/o/r.git", "http://github.com/o/r.git", "https://a.corp.example/", "https://corp.example/",
		"https://notcorp.example/", "https://x.internal.example/", "https://internal.example/", "https://build.example/",
		"https://mirror.example/", "https://10.9.8.7/", "https://11.0.0.1/", "https://192.0.2.7/", "https://[2001:db8::1]/",
		"https://169.254.3.4/", "http://localhost:8080/", "https://127.0.0.1/",
	} {
		target := proxyURL(t, raw)
		want, err := config.ProxyFor(target)
		testutil.FailErr(t, name+" host decision "+raw, err)
		got, err := git(target)
		testutil.FailErr(t, name+" Git decision "+raw, err)
		if proxyValue(want) != proxyValue(got) {
			t.Fatalf("%s: %s host clients use %q, Git would use %q", name, raw, proxyValue(want), proxyValue(got))
		}
	}
}

func TestPolicyOutsideTheSharedGrammarKeepsThePerRemoteDecision(t *testing.T) {
	inverted := desktop(t, "http://proxy.example:3128", "corp.example")
	inverted.ExceptionsInverted = true
	simple := desktop(t, "http://proxy.example:3128")
	simple.Source = systemproxy.SourceMacOS
	simple.Exceptions = append(simple.Exceptions, systemproxy.Exception{Kind: systemproxy.ExceptionSimpleHostnames, Raw: "ExcludeSimpleHostnames"})
	for name, tc := range map[string]struct {
		config httpclient.ProxyConfiguration
		want   []ProxyPlanReason
	}{
		"wildcard inside a name": {
			config: httpclient.ProxyConfiguration{System: desktop(t, "http://proxy.example:3128", "corp.example", "build-*.example")},
			want:   []ProxyPlanReason{{Kind: ProxyReasonException, Detail: "build-*.example"}},
		},
		"subdomains-only environment entry": {
			config: httpclient.ProxyConfiguration{EnvironmentExceptions: []systemproxy.Exception{{Kind: systemproxy.ExceptionSubdomains, Domain: "corp.example", Raw: ".corp.example"}},
				System: desktop(t, "http://proxy.example:3128")},
			want: []ProxyPlanReason{{Kind: ProxyReasonException, Detail: ".corp.example"}},
		},
		"port-specific entry": {
			config: httpclient.ProxyConfiguration{EnvironmentExceptions: []systemproxy.Exception{{Kind: systemproxy.ExceptionDomain, Domain: "corp.example", Port: "8443", Raw: "corp.example:8443"}},
				System: desktop(t, "http://proxy.example:3128")},
			want: []ProxyPlanReason{{Kind: ProxyReasonException, Detail: "corp.example:8443"}},
		},
		"simple host names": {
			config: httpclient.ProxyConfiguration{System: simple},
			want:   []ProxyPlanReason{{Kind: ProxyReasonException, Detail: "ExcludeSimpleHostnames"}},
		},
		"inverted exceptions": {
			config: httpclient.ProxyConfiguration{System: inverted},
			want:   []ProxyPlanReason{{Kind: ProxyReasonInverted, Detail: "gsettings"}},
		},
		"mixed sources": {
			config: httpclient.ProxyConfiguration{EnvironmentHTTPS: proxyURL(t, "http://env.example:8080"), System: desktop(t, "http://proxy.example:3128", "corp.example")},
			want:   []ProxyPlanReason{{Kind: ProxyReasonMixedSources, Detail: "gsettings"}},
		},
		"unreadable system configuration": {
			config: httpclient.ProxyConfiguration{EnvironmentHTTPS: proxyURL(t, "http://env.example:8080"), SystemErr: errors.New("settings tool failed")},
			want:   []ProxyPlanReason{{Kind: ProxyReasonSystemUnavailable, Detail: "settings tool failed"}},
		},
	} {
		plan, err := planFor(tc.config, "https://github.com/o/r.git")
		testutil.FailErr(t, name, err)
		if plan.Mode != ProxyPlanPerRemote || !slices.Equal(plan.Reasons, tc.want) {
			t.Fatalf("%s: plan = %+v, want reasons %+v", name, plan, tc.want)
		}
		decision, err := tc.config.ProxyFor(proxyURL(t, "https://github.com/o/r.git"))
		testutil.FailErr(t, name+" decision", err)
		env := environmentOf(plan.Environment())
		if env["https_proxy"] != proxyValue(decision) || env["http_proxy"] != proxyValue(decision) || env["no_proxy"] != "" {
			t.Fatalf("%s: per-remote environment %v for decision %v", name, env, decision)
		}
	}

	excepted := httpclient.ProxyConfiguration{System: desktop(t, "http://proxy.example:3128", "github.com", "build-*.example")}
	plan, err := planFor(excepted, "https://github.com/o/r.git")
	testutil.FailErr(t, "excepted remote", err)
	if env := environmentOf(plan.Environment()); env["https_proxy"] != "" || env["HTTPS_PROXY"] != "" {
		t.Fatalf("excepted remote environment = %v", env)
	}
	plan, err = planFor(excepted, "ssh://git@github.com/o/r.git")
	testutil.FailErr(t, "ssh remote", err)
	if plan.Mode != ProxyPlanPerRemote || len(plan.Environment()) != 0 {
		t.Fatalf("ssh remote plan = %+v", plan)
	}

	auto := httpclient.ProxyConfiguration{System: systemproxy.Configuration{Source: systemproxy.SourceGSettings, Mode: systemproxy.ModeAuto}}
	if plan, err := planFor(auto, "ssh://git@github.com/o/r.git"); err != nil || plan.Reasons[0].Kind != ProxyReasonAutomatic {
		t.Fatalf("automatic configuration over ssh = %+v, %v", plan, err)
	}
	var unsupported *systemproxy.Unsupported
	if _, err := planFor(auto, "https://github.com/o/r.git"); !errors.As(err, &unsupported) {
		t.Fatalf("automatic configuration over https = %v, want the typed refusal", err)
	}
}

func TestGitLFSReachesItsOwnHostThroughTheProxyWhenTheRemoteIsExcepted(t *testing.T) {
	useBundledGit(t)
	var mu sync.Mutex
	var hosts []string
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(hosts)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.URL.Host)
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	proxyConfigurationFn = func() httpclient.ProxyConfiguration {
		return httpclient.ProxyConfiguration{System: desktop(t, proxy.URL, "git.invalid")}
	}

	dir := t.TempDir()
	initRepo(t, dir)
	remote := "http://git.invalid/r.git"
	opts := Opts{Profile: ProfileNetwork, RemoteURL: remote, ExtraConfig: []string{"lfs.url=http://lfs.invalid/r.git/info/lfs"}}
	if _, _, err := Run(t.Context(), dir, []string{"ls-remote", "--", remote}, opts); err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if slices.Contains(seen(), "git.invalid") {
		t.Fatalf("the excepted remote went through the proxy: %q", seen())
	}
	output, _, err := Run(t.Context(), dir, []string{"lfs", "locks"}, opts)
	if err != nil {
		t.Fatalf("lfs locks: %v", err)
	}
	if !slices.Contains(seen(), "lfs.invalid") {
		t.Fatalf("Git LFS did not use the proxy for its own host: %q\n%s", seen(), output)
	}
}

func TestNetworkInvocationReachesRemoteThroughResolvedProxy(t *testing.T) {
	useBundledGit(t)
	var mu sync.Mutex
	var seen []string
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	proxyConfigurationFn = func() httpclient.ProxyConfiguration {
		return httpclient.ProxyConfiguration{System: desktop(t, proxy.URL)}
	}

	remote := "http://git.invalid/r.git"
	lsRemote := func() error {
		_, _, err := Run(t.Context(), t.TempDir(), []string{"ls-remote", "--", remote}, Opts{Profile: ProfileNetwork, RemoteURL: remote})
		return err
	}
	if err := lsRemote(); err != nil {
		t.Fatalf("ls-remote through the fixture proxy: %v", err)
	}
	if !slices.Contains(requests(), "http://git.invalid/r.git/info/refs?service=git-upload-pack") {
		t.Fatalf("proxy saw %q, want the remote's ref advertisement", requests())
	}

	proxyConfigurationFn = func() httpclient.ProxyConfiguration {
		return httpclient.ProxyConfiguration{System: systemproxy.Configuration{Source: systemproxy.SourceGSettings, Mode: systemproxy.ModeAuto}}
	}
	before := len(requests())
	var unsupported *systemproxy.Unsupported
	if err := lsRemote(); !errors.As(err, &unsupported) || len(requests()) != before {
		t.Fatalf("refused proxy mechanism: err=%v, proxy requests %d -> %d", err, before, len(requests()))
	}
}

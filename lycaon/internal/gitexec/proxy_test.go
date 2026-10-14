package gitexec

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/systemproxy"
)

func TestProxyEnvironmentFollowsHostResolutionForHTTPRemotes(t *testing.T) {
	desktop := &url.URL{Scheme: "http", Host: "proxy.example:3128", User: url.UserPassword("alice", "secret")}
	var asked []string
	proxyResolverFn = func(target *url.URL) (*url.URL, error) {
		asked = append(asked, target.String())
		if target.Hostname() == "direct.example" {
			return nil, nil
		}
		return desktop, nil
	}
	t.Cleanup(func() { proxyResolverFn = httpclient.ResolveProxy })
	for _, tc := range []struct {
		remote string
		want   []string
	}{
		{"https://github.com/o/r.git", []string{"https_proxy=http://alice:secret@proxy.example:3128"}},
		{"HTTP://git.example/r.git", []string{"http_proxy=http://alice:secret@proxy.example:3128"}},
		{"https://direct.example/r.git", nil},
		{"ssh://git@github.com/o/r.git", nil},
		{"git@github.com:o/r.git", nil},
		{"file:///srv/r.git", nil},
		{"", nil},
	} {
		got, err := proxyEnv(tc.remote)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("proxyEnv(%q) = %q, %v; want %q", tc.remote, got, err, tc.want)
		}
	}
	want := []string{"https://github.com/o/r.git", "HTTP://git.example/r.git", "https://direct.example/r.git"}
	if !slices.Equal(asked, want) {
		t.Fatalf("resolved %q, want only HTTP remotes %q", asked, want)
	}

	refused := &systemproxy.Unsupported{Source: systemproxy.SourceGSettings, Mechanism: "proxy auto-discovery"}
	proxyResolverFn = func(*url.URL) (*url.URL, error) { return nil, refused }
	if _, err := proxyEnv("https://github.com/o/r.git"); !errors.Is(err, refused) {
		t.Fatalf("refused mechanism = %v, want the typed refusal", err)
	}
}

func TestNetworkInvocationReachesRemoteThroughResolvedProxy(t *testing.T) {
	useBundledGit(t)
	for _, name := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
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
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyResolverFn = func(*url.URL) (*url.URL, error) { return proxyURL, nil }

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

	proxyResolverFn = func(*url.URL) (*url.URL, error) {
		return nil, &systemproxy.Unsupported{Source: systemproxy.SourceGSettings, Mechanism: "proxy auto-discovery"}
	}
	before := len(requests())
	var unsupported *systemproxy.Unsupported
	if err := lsRemote(); !errors.As(err, &unsupported) || len(requests()) != before {
		t.Fatalf("refused proxy mechanism: err=%v, proxy requests %d -> %d", err, before, len(requests()))
	}
}

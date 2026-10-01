package webresearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/egress"
)

// allowLoopbackFetch permits loopback during a test.
func allowLoopbackFetch(t *testing.T) {
	t.Helper()
	prev := ipAllowed
	ipAllowed = func(netip.Addr) bool { return true }
	t.Cleanup(func() { ipAllowed = prev })
}

func TestIPPublic(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"172.16.0.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"100.64.0.1", false},      // CGNAT
		{"0.0.0.0", false},
		{"::1", false},
		{"fc00::1", false},          // IPv6 ULA
		{"fe80::1", false},          // IPv6 link-local
		{"::ffff:127.0.0.1", false}, // IPv4-mapped loopback
		{"::ffff:10.0.0.1", false},  // IPv4-mapped private
		{"fec0::1", false},          // site-local
		{"2001::1", false},          // Teredo
		{"100::1", false},           // discard-only
		{"64:ff9b::7f00:1", false},  // NAT64 embedding 127.0.0.1
		{"64:ff9b::a00:1", false},   // NAT64 embedding 10.0.0.1
		{"64:ff9b::808:808", true},  // NAT64 embedding 8.8.8.8 (DNS64)
		{"2002:7f00:1::", false},    // 6to4 embedding 127.0.0.1
		{"2002:808:808::", true},    // 6to4 embedding 8.8.8.8
		{"2001:db8::1", false},      // reserved documentation range
	}
	for _, c := range cases {
		addr := netip.MustParseAddr(c.ip)
		if got := egress.IPPublic(addr); got != c.want {
			t.Errorf("IPPublic(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// The development harness reaches its own loopback fixtures and nothing else
// that is not public.
func TestHarnessAllowsLoopbackOnly(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	if !configdir.IsHarnessChannel() {
		t.Skip("harness channel requires a development build")
	}
	for ip, want := range map[string]bool{
		"127.0.0.1":        true,
		"::1":              true,
		"::ffff:127.0.0.1": true,
		"8.8.8.8":          true,
		"10.0.0.1":         false,
		"192.168.1.1":      false,
		"169.254.169.254":  false,
		"fc00::1":          false,
	} {
		if got := ipAllowed(netip.MustParseAddr(ip)); got != want {
			t.Errorf("harness ipAllowed(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestNormalizeFetchURL(t *testing.T) {
	if _, err := normalizeFetchURL("ftp://example.com/x"); err == nil {
		t.Error("ftp scheme should be rejected")
	}
	if _, err := normalizeFetchURL("https://"); err == nil {
		t.Error("missing host should be rejected")
	}
	u, err := normalizeFetchURL("https://user:secret@example.com/path?q=1")
	if err != nil {
		t.Fatalf("valid url: %v", err)
	}
	if u.User != nil {
		t.Errorf("credentials must be stripped, got %v", u.User)
	}
	if strings.Contains(u.String(), "secret") {
		t.Errorf("normalized url leaks credentials: %s", u)
	}
}

func TestResolvePublicIPsBlocksPrivateLiteral(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "[::1]"} {
		h := strings.Trim(host, "[]")
		if _, err := resolvePublicIPs(context.Background(), h); err == nil {
			t.Errorf("resolvePublicIPs(%s) should be blocked", host)
		}
	}
}

func TestFetchURLBlocksLoopbackBySSRF(t *testing.T) {
	// Default policy (no allowLoopbackFetch) must refuse a loopback server.
	srv := testHTTPServer(t, "text/plain", "should not be reachable")
	_, err := FetchURL(context.Background(), FetchOptions{URL: srv})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected SSRF block, got err=%v", err)
	}
}

func TestGuardedGetRevalidatesRedirectHops(t *testing.T) {
	// Permit loopback only, so the httptest server is reachable but a redirect
	// escaping to any non-loopback address must be blocked at the hop boundary.
	prev := ipAllowed
	ipAllowed = func(a netip.Addr) bool { return a.IsLoopback() }
	t.Cleanup(func() { ipAllowed = prev })

	mux := http.NewServeMux()
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, err := normalizeFetchURL(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("normalize metadata url: %v", err)
	}
	//nolint:bodyclose // the assertion is that guardedGet errors before any response exists
	if _, _, err := guardedGet(context.Background(), u, acceptHeaderForMode("text")); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("redirect to metadata address must be blocked, got err=%v", err)
	}

	u, err = normalizeFetchURL(srv.URL + "/hop")
	if err != nil {
		t.Fatalf("normalize hop url: %v", err)
	}
	resp, finalURL, err := guardedGet(context.Background(), u, acceptHeaderForMode("text"))
	if err != nil {
		t.Fatalf("same-host redirect should be followed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasSuffix(finalURL, "/final") {
		t.Fatalf("expected 200 at /final, got %d at %s", resp.StatusCode, finalURL)
	}
}

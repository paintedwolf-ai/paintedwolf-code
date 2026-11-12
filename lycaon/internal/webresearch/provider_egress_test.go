package webresearch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateProviderEndpointBlocksMetadataAndLoopback(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		allowPrivate bool
		wantErrSub   string
	}{
		{
			name:       "metadata literal without private",
			raw:        "https://169.254.169.254/latest/meta-data/",
			wantErrSub: "bare IP",
		},
		{
			name:         "metadata still blocked with private opt-in",
			raw:          "http://169.254.169.254/latest/meta-data/",
			allowPrivate: true,
			wantErrSub:   "not an allowed provider address",
		},
		{
			name:       "loopback literal",
			raw:        "https://127.0.0.1:8080/",
			wantErrSub: "bare IP",
		},
		{
			name:         "loopback still blocked with private opt-in",
			raw:          "http://127.0.0.1:8080/",
			allowPrivate: true,
			wantErrSub:   "not an allowed provider address",
		},
		{
			name:       "file scheme",
			raw:        "file:///etc/passwd",
			wantErrSub: "http or https",
		},
		{
			name:       "http without private opt-in",
			raw:        "http://search.example.com/",
			wantErrSub: "must use https",
		},
		{
			name:         "lan searx allowed",
			raw:          "http://192.168.1.10/searxng",
			allowPrivate: true,
		},
		{
			name: "public https hostname shape ok (no resolve here)",
			raw:  "https://search.brave.com/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateProviderEndpoint(tc.raw, tc.allowPrivate)
			if tc.wantErrSub == "" {
				if err != nil {
					t.Fatalf("validateProviderEndpoint(%q) unexpected err: %v", tc.raw, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("validateProviderEndpoint(%q) err=%v want substring %q", tc.raw, err, tc.wantErrSub)
			}
		})
	}
}

func TestValidateProviderConfigEndpointAcceptsLANSearx(t *testing.T) {
	err := ValidateProviderConfigEndpoint(context.Background(), "http://192.168.1.10/searxng", true)
	if err != nil {
		t.Fatalf("LAN searx: %v", err)
	}
	err = ValidateProviderConfigEndpoint(context.Background(), "http://127.0.0.1/searxng", true)
	if err == nil {
		t.Fatal("expected loopback searx reject")
	}
	err = ValidateProviderConfigEndpoint(context.Background(), "https://127.0.0.1/search", false)
	if err == nil {
		t.Fatal("expected bare loopback reject without private")
	}
}

func TestDoProviderHTTPBlocksDisallowedTarget(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://169.254.169.254/", nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	_, _, err = doProviderHTTP(context.Background(), req, 2, false)
	if err == nil {
		t.Fatal("expected metadata block")
	}
	if !strings.Contains(err.Error(), "https") && !strings.Contains(err.Error(), "bare IP") &&
		!strings.Contains(err.Error(), "blocked") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDoProviderHTTPPinsValidatedPublicTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`ok`))
	}))
	t.Cleanup(srv.Close)
	allowLoopbackFetch(t)
	prevRelax := providerEgressTestRelax
	providerEgressTestRelax = true
	t.Cleanup(func() { providerEgressTestRelax = prevRelax })

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	body, status, err := doProviderHTTP(context.Background(), req, 5, false)
	if err != nil {
		t.Fatalf("doProviderHTTP: %v", err)
	}
	if status != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", status, body)
	}
}

func TestSearxngCatalogAllowsPrivateEndpoint(t *testing.T) {
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog failed", err)
	entry, ok := cat.Entry("searxng")
	if !ok || !entry.AllowPrivateEndpoint {
		t.Fatalf("searxng allow_private_endpoint = %+v", entry)
	}
	entry, ok = cat.Entry("brave")
	if !ok || entry.AllowPrivateEndpoint {
		t.Fatalf("brave must not allow private endpoints: %+v", entry)
	}
}

// A provider redirect meets the endpoint policy its first hop did.
func TestProviderHopsMeetTheEndpointPolicy(t *testing.T) {
	hop := providerHop(false)
	for _, raw := range []string{"http://api.example.test/next", "https://203.0.113.7/next"} {
		u, err := url.Parse(raw)
		testutil.FailErr(t, "parse", err)
		if err := hop(context.Background(), u); err == nil {
			t.Fatalf("hop to %s was allowed", raw)
		}
	}
	u, err := url.Parse("https://api.example.test/next")
	testutil.FailErr(t, "parse", err)
	if err := hop(context.Background(), u); err != nil {
		t.Fatalf("hop to an https hostname: %v", err)
	}
}

// A provider body past the read bound is a prefix, as a bounded read was.
func TestDoProviderHTTPReadsAPrefixOfALargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), providerBodyMax+64))
	}))
	t.Cleanup(srv.Close)
	allowLoopbackFetch(t)
	prevRelax := providerEgressTestRelax
	providerEgressTestRelax = true
	t.Cleanup(func() { providerEgressTestRelax = prevRelax })

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "build request", err)
	body, status, err := doProviderHTTP(context.Background(), req, 5, false)
	testutil.FailErr(t, "doProviderHTTP", err)
	if status != 200 || len(body) != providerBodyMax {
		t.Fatalf("status %d, %d body bytes, want %d", status, len(body), providerBodyMax)
	}
}

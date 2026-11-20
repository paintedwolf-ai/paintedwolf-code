package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/hostproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFixedModelDownloadRequiresPublicAddresses(t *testing.T) {
	egress.TestingResolve(t, egress.StaticLookup(netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")))
	_, client, err := FixedPublicDownload(t.Context(), egressclass.LocalModelDownload, time.Minute)
	var denied *egress.DestinationDeniedError
	if client != nil || !errors.As(err, &denied) {
		t.Fatalf("mixed DNS answers: %v", err)
	}
}

func TestFixedModelDownloadUsesPinnedAddressThroughProxy(t *testing.T) {
	seen := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.Host
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	lookups := 0
	egress.TestingResolve(t, func(_ context.Context, _, host string) ([]netip.Addr, error) {
		lookups++
		if host != "downloads.paintedwolf.dev" {
			t.Fatalf("unexpected destination %q", host)
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	origin, client, err := FixedPublicDownload(t.Context(), egressclass.LocalModelDownload, time.Minute)
	testutil.FailErr(t, "build pinned model download client", err)
	defer client.CloseIdleConnections()
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect result: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/manifest.json", nil)
	testutil.FailErr(t, "prepare download", err)
	if _, err := client.Do(req); !errors.Is(err, hostproxy.ProxyRejected) {
		t.Fatalf("proxy outcome: %v", err)
	}
	if got := <-seen; got != "CONNECT 8.8.8.8:443" || lookups != 1 {
		t.Fatalf("proxy destination %q after %d lookups", got, lookups)
	}
}

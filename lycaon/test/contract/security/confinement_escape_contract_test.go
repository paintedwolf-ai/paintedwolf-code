package contract

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The egress broker refuses callers it cannot attribute to a live action.
func TestConfinementIntegrityUnattributedProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	deciderCalled := false
	var resolveOK bool
	p := newTestBrokerFor(func(_ context.Context, _ string, _ egressproxy.Endpoint) bool {
		deciderCalled = true
		return true
	}, func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		if !resolveOK {
			return egressproxy.Peer{}, false
		}
		return egressproxy.Peer{Lineage: "live-command", Leased: true}, true
	})
	p.SetDialEndpointForTest(func(ctx context.Context, _ egressproxy.Endpoint) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	})
	_, err := p.Start()
	testutil.FailErr(t, "proxy Start", err)
	t.Cleanup(func() { _ = p.Close() })

	pu, err := url.Parse("http://" + p.Addr())
	testutil.FailErr(t, "parse proxy", err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(upstream.URL)
	testutil.FailErr(t, "proxy GET", err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unattributed caller must get 403, got %d", resp.StatusCode)
	}
	if deciderCalled {
		t.Fatal("decider must not run for unattributed callers")
	}

	resolveOK = true
	liveResp, err := client.Get(upstream.URL)
	testutil.FailErr(t, "live GET", err)
	body, _ := io.ReadAll(liveResp.Body)
	_ = liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("positive control: attributed peer must forward, got %d %q", liveResp.StatusCode, body)
	}
}

// newTestBrokerFor builds a broker that already knows who is calling it.
func newTestBrokerFor(decide egressproxy.EndpointDecider, resolve egressproxy.PeerResolver) *egressproxy.Broker {
	b := egressproxy.New(decide)
	b.SetPeerResolver(resolve)
	return b
}

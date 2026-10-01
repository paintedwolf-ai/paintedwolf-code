package sshproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProxyCommandRelaysThroughSocks(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "upstream listen", err)
	defer func() { _ = upstream.Close() }()

	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "pong")
	}()

	p := newTestBrokerFor(func(context.Context, string, egressproxy.Endpoint) bool {
		return true
	}, leasedPeer("cmd"))
	p.SetDialEndpointForTest(func(ctx context.Context, ep egressproxy.Endpoint) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ep.DialAddr())
	})
	addrs, err := p.Start()
	testutil.FailErr(t, "start proxy", err)
	defer func() { _ = p.Close() }()

	t.Setenv("LYCAON_SOCKS_PROXY", addrs.SOCKS)
	t.Setenv("LYCAON_PROXY_TOKEN", "tok")

	host, port, err := net.SplitHostPort(upstream.Addr().String())
	testutil.FailErr(t, "split upstream", err)

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := runProxyCommand(ctx, host, port, bytes.NewReader(nil), &out); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.DeadlineExceeded) {
		// Relay ends when upstream closes; connect/auth failures are fatal.
		if out.Len() == 0 {
			t.Fatalf("proxy command: %v", err)
		}
	}
	if got := out.String(); got != "pong" {
		t.Fatalf("relayed %q want pong (err=%v)", got, err)
	}
}

// leasedPeer resolves every caller to one live action; attribution itself is
// covered by the broker's own tests.
func leasedPeer(lineage string) egressproxy.PeerResolver {
	return func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		return egressproxy.Peer{Lineage: lineage, Leased: true}, true
	}
}

// newTestBrokerFor builds a broker that already knows who is calling it.
func newTestBrokerFor(decide egressproxy.EndpointDecider, resolve egressproxy.PeerResolver) *egressproxy.Broker {
	b := egressproxy.New(decide)
	b.SetPeerResolver(resolve)
	return b
}

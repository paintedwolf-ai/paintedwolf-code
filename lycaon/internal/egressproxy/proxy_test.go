package egressproxy_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func clientVia(proxyAddr string) *http.Client {
	pu, _ := url.Parse("http://" + proxyAddr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
}

// leasedPeer resolves every caller to one live action, isolating destination
// policy from caller attribution.
func leasedPeer(lineage string) egressproxy.PeerResolver {
	return func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		return egressproxy.Peer{Lineage: lineage, Leased: true, Owner: "command running fixture"}, true
	}
}

// brokerWith builds a broker that already knows who is calling it.
func brokerWith(decide egressproxy.EndpointDecider, resolve egressproxy.PeerResolver) *egressproxy.Broker {
	b := egressproxy.New(decide)
	b.SetPeerResolver(resolve)
	return b
}

func allowLocalDial(p *egressproxy.Broker) {
	p.SetDialEndpointForTest(func(ctx context.Context, ep egressproxy.Endpoint) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ep.DialAddr())
	})
}

func TestProxyAllowsAndDeniesByHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	var asked []string
	p := brokerWith(func(_ context.Context, _ string, endpoint egressproxy.Endpoint) bool {
		asked = append(asked, endpoint.Host)
		return endpoint.Host == "127.0.0.1"
	}, leasedPeer("cmd"))
	allowLocalDial(p)
	if _, err := p.Start(); err != nil {
		testutil.FailErr(t, "p.Start failed", err)
	}
	defer func() { _ = p.Close() }()

	client := clientVia(p.Addr())

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("allowed request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("allowed host should forward, got %d %q", resp.StatusCode, body)
	}

	// Destination denials return an HTTP response without an upstream connection.
	resp2, err := client.Get("http://denied.example/")
	if err != nil {
		t.Fatalf("request to denied host should reach the proxy, got transport error: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("denied host should get 403, got %d", resp2.StatusCode)
	}

	if len(asked) == 0 {
		t.Fatal("decider was never consulted")
	}
}

// The broker serves processes it can place, and nobody else. A caller it
// cannot resolve never reaches destination policy at all.
func TestBrokerRefusesCallersItCannotAttribute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	deciderCalled := false
	unresolvable := func(_, _ netip.AddrPort) (egressproxy.Peer, bool) { return egressproxy.Peer{}, false }
	p := brokerWith(func(_ context.Context, _ string, _ egressproxy.Endpoint) bool {
		deciderCalled = true
		return true
	}, unresolvable)
	allowLocalDial(p)
	var refused []egressproxy.Refusal
	p.SetRefusalReporter(func(_ egressproxy.Peer, _ egressproxy.Endpoint, r egressproxy.Refusal) {
		refused = append(refused, r)
	})
	if _, err := p.Start(); err != nil {
		testutil.FailErr(t, "p.Start failed", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get(upstream.URL)
	if err != nil {
		t.Fatalf("request should reach the proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unattributable request status = %d, want 403", resp.StatusCode)
	}
	if deciderCalled {
		t.Fatal("destination policy ran for a caller the broker could not place")
	}
	if len(refused) != 1 || refused[0] != egressproxy.RefusedUnattributable {
		t.Fatalf("refusals = %v, want one %q", refused, egressproxy.RefusedUnattributable)
	}
}

// A process an earlier action left running gets a refusal that names where it
// came from, so the failure explains itself instead of surfacing as a bare
// connection error.
func TestBrokerNamesTheActionThatLeftAProcessRunning(t *testing.T) {
	ended := func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		return egressproxy.Peer{Lineage: "old", Owner: "command running ./task check", Leased: false}, true
	}
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, ended)
	var got egressproxy.Peer
	var refusal egressproxy.Refusal
	p.SetRefusalReporter(func(peer egressproxy.Peer, _ egressproxy.Endpoint, r egressproxy.Refusal) {
		got, refusal = peer, r
	})
	if _, err := p.Start(); err != nil {
		testutil.FailErr(t, "p.Start failed", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get("http://vuln.go.dev/index.json")
	if err != nil {
		t.Fatalf("request should reach the proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if refusal != egressproxy.RefusedLeaseEnded {
		t.Fatalf("refusal = %q, want %q", refusal, egressproxy.RefusedLeaseEnded)
	}
	if got.Owner != "command running ./task check" {
		t.Fatalf("refusal lost the owning action: %+v", got)
	}
	if !strings.Contains(string(body), "./task check") {
		t.Fatalf("refusal body does not name the action that left the process running: %s", body)
	}
}

func TestBrokerPassesTheResolvedLineageToTheDecider(t *testing.T) {
	var gotProxyAuthorization, gotRequestHop string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProxyAuthorization = r.Header.Get("Proxy-Authorization")
		gotRequestHop = r.Header.Get("X-Request-Hop")
		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "remove-me")
	}))
	defer upstream.Close()

	var gotLineage string
	p := brokerWith(func(_ context.Context, lineage string, _ egressproxy.Endpoint) bool {
		gotLineage = lineage
		return true
	}, leasedPeer("action-lineage-123"))
	allowLocalDial(p)
	if _, err := p.Start(); err != nil {
		testutil.FailErr(t, "p.Start failed", err)
	}
	defer func() { _ = p.Close() }()

	// A caller-supplied credential is ignored; identity comes from the peer.
	pu, _ := url.Parse("http://forged:x@" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	testutil.FailErr(t, "NewRequest", err)
	req.Header.Set("Connection", "X-Request-Hop")
	req.Header.Set("X-Request-Hop", "remove-me")
	resp, err := client.Do(req)
	testutil.FailErr(t, "client.Get failed", err)
	_ = resp.Body.Close()
	if gotLineage != "action-lineage-123" {
		t.Fatalf("decider received %q; identity must come from the resolved peer", gotLineage)
	}
	if gotProxyAuthorization != "" || gotRequestHop != "" {
		t.Fatalf("hop headers reached upstream: proxy-auth=%q request-hop=%q", gotProxyAuthorization, gotRequestHop)
	}
	if got := resp.Header.Get("X-Response-Hop"); got != "" {
		t.Fatalf("response hop header reached client: %q", got)
	}
}

func TestHTTPRechecksTheLeaseAfterAParkedDecision(t *testing.T) {
	decisionStarted := make(chan struct{})
	releaseDecision := make(chan struct{})
	var live atomic.Bool
	live.Store(true)
	dialed := false
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool {
		close(decisionStarted)
		<-releaseDecision
		return true
	}, leasedPeer("live"))
	p.SetLeaseCheck(func(string) bool { return live.Load() })
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		dialed = true
		return nil, io.EOF
	})
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	client := clientVia(p.Addr())
	result := make(chan int, 1)
	go func() {
		resp, _ := client.Get("http://parked.example/")
		if resp == nil {
			result <- 0
			return
		}
		defer func() { _ = resp.Body.Close() }()
		result <- resp.StatusCode
	}()
	<-decisionStarted
	live.Store(false)
	close(releaseDecision)
	status := <-result
	if status == 0 {
		t.Fatal("expected proxy response")
	}
	if status != http.StatusForbidden {
		t.Fatalf("status=%d want 403", status)
	}
	if dialed {
		t.Fatal("an ended lease dialed after a parked decision")
	}
}

func TestHTTPClosesConnectionWhenTheLeaseEndsDuringDial(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("live"))
	p.SetLeaseCheck(func(string) bool { return live.Load() })
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		client, server := net.Pipe()
		_ = server.Close()
		live.Store(false)
		return client, nil
	})
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get("http://dial-race.example/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
}

func TestHTTPDefaultDialBlocksPrivateLANLiteral(t *testing.T) {
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("cmd"))
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get("http://10.0.0.1:1/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not an allowed address") {
		t.Fatalf("private-address denial body=%q", body)
	}
}

func serverPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	testutil.FailErr(t, "parse upstream url", err)
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	testutil.FailErr(t, "parse upstream port", err)
	return uint16(port)
}

// Host approval and local port authority are checked independently.
func TestProxyRefusesLoopbackWithoutTheLoopbackConnectGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local")
	}))
	defer upstream.Close()
	decided := false
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool {
		decided = true
		return true
	}, leasedPeer("cmd"))
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get(upstream.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !decided {
		t.Fatal("decider was never consulted")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted loopback relay status=%d body=%q, want 403", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), egressproxy.BlockedMarker) ||
		!strings.Contains(string(body), "loopback_connect") {
		t.Fatalf("refusal must name the axis and its recovery, got %q", body)
	}
}

// CONNECT tunnels require local port authority before relaying bytes.
func TestProxyRefusesLoopbackConnectTunnelWithoutTheGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("cmd"))
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", p.Addr())
	testutil.FailErr(t, "dial proxy", err)
	defer func() { _ = conn.Close() }()
	target := fmt.Sprintf("127.0.0.1:%d", serverPort(t, upstream.URL))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	testutil.FailErr(t, "write CONNECT", err)
	status, err := bufio.NewReader(conn).ReadString('\n')
	testutil.FailErr(t, "read CONNECT status", err)
	if !strings.Contains(status, "403") {
		t.Fatalf("ungranted loopback tunnel status=%q, want 403", strings.TrimSpace(status))
	}
}

// The broker enforces the applied profile's local port grant.
func TestProxyMediatesLoopbackWithinTheGrantedPorts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local")
	}))
	defer upstream.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "other")
	}))
	defer other.Close()

	granted := serverPort(t, upstream.URL)
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("cmd"))
	p.SetLoopbackConnectAuthority(func(_ string, port uint16) bool { return port == granted })
	if _, err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	resp, err := clientVia(p.Addr()).Get(upstream.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "local" {
		t.Fatalf("granted port status=%d body=%q", resp.StatusCode, body)
	}

	resp2, err := clientVia(p.Addr()).Get(other.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("port outside the grant status=%d, want 403", resp2.StatusCode)
	}
}

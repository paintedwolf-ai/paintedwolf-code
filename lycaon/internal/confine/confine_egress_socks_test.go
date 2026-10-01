package confine

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestBrokerSocksPortKeying(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	asks := 0
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asks++
		return ep.Port == 5432
	}
	ep5432 := egressproxy.Endpoint{Host: "db.example", Port: 5432, Transport: egressproxy.TransportSocksTCP}
	ep22 := egressproxy.Endpoint{Host: "db.example", Port: 22, Transport: egressproxy.TransportSocksTCP}
	if !b.decideEndpoint(context.Background(), "tok", ep5432) {
		t.Fatal("5432 should allow")
	}
	if b.decideEndpoint(context.Background(), "tok", ep22) {
		t.Fatal("22 must not inherit 5432 approval")
	}
	if asks != 2 {
		t.Fatalf("asks=%d want 2", asks)
	}
}

func TestBrokerSocksCoalesceSamePort(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	release := make(chan struct{})
	var asked atomic.Int32
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		asked.Add(1)
		<-release
		return true
	}
	ep := egressproxy.Endpoint{Host: "shared.example", Port: 9000, Transport: egressproxy.TransportSocksTCP}
	done := make(chan bool, 2)
	go func() { done <- b.decideEndpoint(context.Background(), "tok", ep) }()
	for asked.Load() == 0 {
	}
	go func() { done <- b.decideEndpoint(context.Background(), "tok", ep) }()
	close(release)
	first, second := <-done, <-done
	if !first || !second {
		t.Fatal("both waiters should allow")
	}
	if asked.Load() != 1 {
		t.Fatalf("asked=%d want 1", asked.Load())
	}
}

// Approval identity for a CONNECT tunnel is host and port, so an answer for
// api.example:443 does not authorize api.example:22.
func TestEgressVerdictKeyConnectCarriesPort(t *testing.T) {
	cmd := EgressCommand{SessionID: "s1"}
	https := egressproxy.Endpoint{Host: "api.example", Transport: egressproxy.TransportHTTPConnect, Port: 443}
	ssh := egressproxy.Endpoint{Host: "api.example", Transport: egressproxy.TransportHTTPConnect, Port: 22}

	got := egressVerdictKey(cmd, "tok", https, nil)
	if want := "endpoint\x00s1\x00tok\x00api.example\x00443\x00http_connect"; got != want {
		t.Fatalf("key=%q want=%q", got, want)
	}
	if other := egressVerdictKey(cmd, "tok", ssh, nil); other == got {
		t.Fatalf("CONNECT to :22 must not reuse the :443 verdict, both %q", got)
	}
}

// The inspectable transport carries the same identity as a tunnel: an answer
// for :80 does not cover :9200, which a redirect reaches without a decision.
func TestEgressVerdictKeyPlainHTTPCarriesPort(t *testing.T) {
	cmd := EgressCommand{SessionID: "s1"}
	web := egressproxy.Endpoint{Host: "api.example", Transport: egressproxy.TransportHTTPRequest, Port: 80}
	search := egressproxy.Endpoint{Host: "api.example", Transport: egressproxy.TransportHTTPRequest, Port: 9200}

	got := egressVerdictKey(cmd, "tok", web, nil)
	if want := "endpoint\x00s1\x00tok\x00api.example\x0080\x00http_request"; got != want {
		t.Fatalf("key=%q want=%q", got, want)
	}
	if other := egressVerdictKey(cmd, "tok", search, nil); other == got {
		t.Fatalf("a redirect to :9200 must not reuse the :80 verdict, both %q", got)
	}
}

func TestEgressVerdictKeySocks(t *testing.T) {
	ep := egressproxy.Endpoint{Host: "db.example", Port: 5432, Transport: egressproxy.TransportSocksTCP}
	want := "endpoint\x00s1\x00tok\x00db.example\x005432\x00socks_tcp"
	if got := egressVerdictKey(EgressCommand{SessionID: "s1"}, "tok", ep, nil); got != want {
		t.Fatalf("key=%q want=%q", got, want)
	}
}

// One destination reached through two front doors is two decisions: the tunnel is
// unreadable and the plain request is not, so they are not the same subject.
func TestEgressVerdictKeySeparatesTransports(t *testing.T) {
	cmd := EgressCommand{SessionID: "s1"}
	plain := egressproxy.Endpoint{Host: "api.example", Port: 443, Transport: egressproxy.TransportHTTPRequest}
	tunnel := egressproxy.Endpoint{Host: "api.example", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	if egressVerdictKey(cmd, "tok", plain, nil) == egressVerdictKey(cmd, "tok", tunnel, nil) {
		t.Fatal("a readable request and an opaque tunnel shared one verdict")
	}
}

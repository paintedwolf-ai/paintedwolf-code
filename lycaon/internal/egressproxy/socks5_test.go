package egressproxy_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSocksNegotiatesNoAuthAndRefusesOtherMethods(t *testing.T) {
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("live"))
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	// The caller sends no credential; the broker reads identity from the peer.
	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		testutil.FailErr(t, "conn.Write failed", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		testutil.FailErr(t, "io.ReadFull failed", err)
	}
	if resp[1] != 0x00 {
		t.Fatalf("no-auth should be selected, got method %d", resp[1])
	}

	// A client offering only user/pass has nothing the broker accepts.
	conn2, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial2", err)
	defer func() { _ = conn2.Close() }()
	if _, err := conn2.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		testutil.FailErr(t, "conn2.Write failed", err)
	}
	if _, err := io.ReadFull(conn2, resp[:]); err != nil {
		testutil.FailErr(t, "io.ReadFull failed", err)
	}
	if resp[1] != 0xff {
		t.Fatalf("user/pass-only should be refused, got method %d", resp[1])
	}
}

func TestSocksRefusesAfterTheLeaseEnds(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	decided := false
	dialed := false
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool {
		decided = true
		return true
	}, leasedPeer("live"))
	p.SetLeaseCheck(func(string) bool { return live.Load() })
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		dialed = true
		return nil, io.EOF
	})
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	if err := socksClientHandshake(conn); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	live.Store(false)
	if err := socksClientConnectDomain(conn, "stale.example", 443); err == nil {
		t.Fatal("an ended lease still connected")
	}
	if decided || dialed {
		t.Fatalf("revoked token reached policy/dial: decided=%t dialed=%t", decided, dialed)
	}
}

func TestSocksRechecksTheLeaseAfterAParkedDecision(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	decisionStarted := make(chan struct{})
	releaseDecision := make(chan struct{})
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
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	if err := socksClientHandshake(conn); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- socksClientConnectDomain(conn, "parked.example", 443) }()
	<-decisionStarted
	live.Store(false)
	close(releaseDecision)
	if err := <-result; err == nil {
		t.Fatal("revoked parked connection succeeded")
	}
	if dialed {
		t.Fatal("revoked parked connection dialed")
	}
}

func TestSocksRejectsNonzeroReservedByte(t *testing.T) {
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool {
		t.Fatal("destination policy reached for malformed request")
		return true
	}, leasedPeer("tok"))
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	if err := socksClientHandshake(conn); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x01, 0x01, 1, 1, 1, 1, 0, 80}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[1] == 0x00 {
		t.Fatal("nonzero reserved byte succeeded")
	}
}

func TestSocksConnectDomainAfterAllow(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen upstream", err)
	defer func() { _ = upstream.Close() }()
	go func() {
		c, _ := upstream.Accept()
		if c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	upPort := uint16(upstream.Addr().(*net.TCPAddr).Port)
	var resolved atomic.Bool
	p := brokerWith(func(_ context.Context, lineage string, ep egressproxy.Endpoint) bool {
		if lineage != "tok" || ep.Host != "proxy-target.test" || ep.Port != upPort {
			return false
		}
		resolved.Store(true)
		return true
	}, leasedPeer("tok"))
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		if !resolved.Load() {
			t.Fatal("dial before allow decision")
		}
		return net.Dial("tcp", upstream.Addr().String())
	})
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial socks", err)
	defer func() { _ = conn.Close() }()
	if err := socksClientHandshake(conn); err != nil {
		testutil.FailErr(t, "socksClientHandshake failed", err)
	}
	port := upPort
	if err := socksClientConnectDomain(conn, "proxy-target.test", port); err != nil {
		testutil.FailErr(t, "socksClientConnectDomain failed", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("relay=%q err=%v", buf, err)
	}
}

func TestSocksDenySkipsDial(t *testing.T) {
	dialed := false
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return false }, leasedPeer("tok"))
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		dialed = true
		return nil, io.EOF
	})
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	_ = socksClientHandshake(conn)
	_ = socksClientConnectDomain(conn, "deny.test", 443)
	if dialed {
		t.Fatal("denied CONNECT must not dial upstream")
	}
}

func TestSocksRejectsBindAndUDPAssociate(t *testing.T) {
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool {
		t.Fatal("policy must not run for unsupported commands")
		return true
	}, leasedPeer("tok"))
	p.SetDialEndpointForTest(func(context.Context, egressproxy.Endpoint) (net.Conn, error) {
		t.Fatal("dial must not run for unsupported commands")
		return nil, io.EOF
	})
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	defer func() { _ = p.Close() }()

	for _, cmd := range []byte{0x02, 0x03} { // BIND, UDP ASSOCIATE
		conn, err := net.Dial("tcp", addrs.SOCKS)
		testutil.FailErr(t, "dial", err)
		if err := socksClientHandshake(conn); err != nil {
			_ = conn.Close()
			t.Fatalf("handshake: %v", err)
		}
		// CONNECT request shape with unsupported CMD.
		req := []byte{0x05, cmd, 0x00, 0x01, 127, 0, 0, 1, 0, 80}
		if _, err := conn.Write(req); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		hdr := make([]byte, 10) // VER REP RSV ATYP + IPv4 + PORT
		if _, err := io.ReadFull(conn, hdr); err != nil {
			_ = conn.Close()
			t.Fatalf("cmd %d reply: %v", cmd, err)
		}
		_ = conn.Close()
		if hdr[1] != 0x07 { // command not supported
			t.Fatalf("cmd %d reply=%d want 0x07", cmd, hdr[1])
		}
	}
}

func TestStartRollbackOnSocksFailure(t *testing.T) {
	p := egressproxy.New(nil)
	calls := 0
	_, err := p.Start(egressproxy.Options{
		ListenHTTP: net.Listen,
		ListenSOCKS: func(network, address string) (net.Listener, error) {
			calls++
			return nil, io.EOF
		},
	})
	if err == nil {
		t.Fatal("expected socks failure")
	}
	if calls != 1 {
		t.Fatalf("socks listen calls=%d", calls)
	}
	if p.Addr() != "" {
		t.Fatal("http listener must not remain published")
	}
	if p.SocksAddr() != "" {
		t.Fatal("socks listener must not remain published")
	}
}

func TestCloseStopsAuthenticatedConnectionAwaitingRequest(t *testing.T) {
	p := brokerWith(nil, leasedPeer("live"))
	addrs, err := p.Start()
	testutil.FailErr(t, "Start", err)
	conn, err := net.Dial("tcp", addrs.SOCKS)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	if err := socksClientHandshake(conn); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited on an authenticated idle connection")
	}
}

func socksClientHandshake(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return err
	}
	if resp[1] != 0x00 {
		return io.EOF
	}
	return nil
}

func socksClientConnectDomain(conn net.Conn, host string, port uint16) error {
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], port)
	req = append(req, p[:]...)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[1] != 0x00 {
		return io.EOF
	}
	// Consume BND.ADDR + BND.PORT from the server reply.
	switch hdr[3] {
	case 0x01:
		_, err := io.ReadFull(conn, make([]byte, 4+2))
		return err
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return err
		}
		_, err := io.ReadFull(conn, make([]byte, int(l[0])+2))
		return err
	case 0x04:
		_, err := io.ReadFull(conn, make([]byte, 16+2))
		return err
	default:
		return io.EOF
	}
}

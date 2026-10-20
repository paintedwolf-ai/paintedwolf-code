package egressproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDerivedLoopbackRechecksResourceBeforeRequestBytes(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	testutil.FailErr(t, "open actual fixture listener", err)
	t.Cleanup(func() { _ = listener.Close() })
	addr := listener.Addr().(*net.TCPAddr).AddrPort()
	endpoint := Endpoint{Host: addr.Addr().String(), Port: addr.Port()}
	var generation atomic.Int64
	generation.Store(1)
	observed := func(_ context.Context, reached netip.AddrPort) (string, bool) {
		if reached != addr {
			t.Errorf("ownership checked wrong endpoint %v != %v", reached, addr)
			return "", false
		}
		if generation.Load() == 1 {
			return "fixture-socket-generation-one", true
		}
		return "fixture-socket-generation-two", true
	}
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		bytes, _ := io.ReadAll(conn)
		received <- bytes
	}()
	revoked := 0
	conn, err := defaultDialEndpoint(t.Context(), endpoint, nil, false, observed, func() { revoked++ })
	testutil.FailErr(t, "dial live derived endpoint", err)
	n, err := conn.Write([]byte("accepted"))
	testutil.FailErr(t, "write while same resource is observed", err)
	if n != len("accepted") {
		t.Fatal("short fixture write")
	}
	generation.Store(2)
	n, err = conn.Write([]byte("must-not-arrive"))
	if n != 0 || !errors.Is(err, ErrLoopbackNotAuthorized) || revoked != 1 {
		t.Fatalf("changed resource delivered bytes n=%d err=%v observations=%d", n, err, revoked)
	}
	_ = conn.Close()
	select {
	case bytes := <-received:
		if string(bytes) != "accepted" {
			t.Fatalf("listener received unauthorized payload %q", bytes)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("actual fixture listener did not settle")
	}
}

func TestDerivedLoopbackRequiresLiveExactObservationBeforeDial(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	testutil.FailErr(t, "open listening foreign fixture", err)
	t.Cleanup(func() { _ = listener.Close() })
	addr := listener.Addr().(*net.TCPAddr).AddrPort()
	endpoint := Endpoint{Host: addr.Addr().String(), Port: addr.Port()}
	checks := 0
	_, err = defaultDialEndpoint(t.Context(), endpoint, nil, false, func(_ context.Context, reached netip.AddrPort) (string, bool) { checks++; return "", false }, nil)
	if !errors.Is(err, ErrLoopbackNotAuthorized) || checks != 1 {
		t.Fatalf("missing live ownership admitted err=%v checks=%d", err, checks)
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("foreign listener accepted an unauthorized connection")
	}
	testutil.FailErr(t, "close listener", listener.Close())
	_, err = defaultDialEndpoint(t.Context(), endpoint, nil, false, func(context.Context, netip.AddrPort) (string, bool) { return "", false }, nil)
	if !errors.Is(err, ErrLoopbackNotAuthorized) {
		t.Fatalf("closed listener retained derived authority: %v", err)
	}
}

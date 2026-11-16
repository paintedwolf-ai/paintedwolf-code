package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func TestConnectionCapacityRejectsBeforeVerificationAndReclaimsSlots(t *testing.T) {
	f := newFixture(t)
	var verified atomic.Int64
	f.server.PeerOf = func(net.Conn) (Peer, error) {
		verified.Add(1)
		return f.getPeer(), nil
	}
	_ = f.start()
	clients := make([]*vaultidentity.Client, 0, maxConnections+1)
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.Close()
		}
	})
	probe := vaultidentity.Request{Op: vaultidentity.OpProbe}
	for range maxConnections {
		client := vaultidentity.NewClient(f.socket)
		clients = append(clients, client)
		if response := do(t, client, probe); response.Status != vaultidentity.StatusOK {
			t.Fatalf("admitted caller was refused: %+v", response)
		}
	}
	overflow, err := net.DialTimeout("unix", f.socket, time.Second)
	testutil.FailErr(t, "connect overflow caller", err)
	defer func() { _ = overflow.Close() }()
	var one [1]byte
	if n, err := overflow.Read(one[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("overflow caller was not immediately closed: bytes=%d error=%v", n, err)
	}
	if got := verified.Load(); got != maxConnections {
		t.Fatalf("overflow caller reached verification: got %d calls, want %d", got, maxConnections)
	}
	if response := do(t, clients[1], probe); response.Status != vaultidentity.StatusOK {
		t.Fatalf("overflow disturbed an admitted caller: %+v", response)
	}
	testutil.FailErr(t, "close admitted caller", clients[0].Close())
	testutil.WaitFor(t, "the closed caller's slot is released", func() bool {
		return f.server.active.Load() != maxConnections
	})
	if got := f.server.active.Load(); got != maxConnections-1 {
		t.Fatalf("closed caller retained its slot: active=%d", got)
	}
	replacement := vaultidentity.NewClient(f.socket)
	clients = append(clients, replacement)
	if response := do(t, replacement, probe); response.Status != vaultidentity.StatusOK {
		t.Fatalf("released slot could not serve a new caller: %+v", response)
	}
	if got := verified.Load(); got != maxConnections+1 {
		t.Fatalf("replacement did not reach verification exactly once: calls=%d", got)
	}
	f.cancel()
	// Cancellation starts shutdown; wait for Serve to finish closing the listener
	// and draining callers before asserting that connections are unusable.
	err = testutil.Receive(t, "the service finishes shutdown", f.done)
	f.done <- err // The fixture cleanup also observes the Serve result.
	if err != nil {
		t.Fatalf("shutdown service: %v", err)
	}
	if got := f.server.active.Load(); got != 0 {
		t.Fatalf("shutdown retained active callers: %d", got)
	}
	for _, client := range clients[1:] {
		if _, err := client.Do(probe); err == nil {
			t.Fatal("saturated service retained a usable connection after shutdown")
		}
	}
}

func TestUnexpectedListenerClosureDrainsVerifiedConnections(t *testing.T) {
	f := newFixture(t)
	listener, err := net.Listen("unix", f.socket)
	testutil.FailErr(t, "listen", err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(ctx, listener) }()
	client := vaultidentity.NewClient(f.socket)
	t.Cleanup(func() { _ = client.Close() })
	probe := vaultidentity.Request{Op: vaultidentity.OpProbe}
	if response := do(t, client, probe); response.Status != vaultidentity.StatusOK {
		t.Fatalf("caller was not verified: %+v", response)
	}
	testutil.FailErr(t, "close listener", listener.Close())
	err = testutil.Receive(t, "the listener failure returns without waiting for the verified client", done)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("unexpected listener failure was lost: %v", err)
	}
	if _, err := client.Do(probe); err == nil {
		t.Fatal("verified connection survived listener failure")
	}
}

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
		return f.peer, nil
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
	testutil.FailErr(t, "bound overflow read", overflow.SetReadDeadline(time.Now().Add(time.Second)))
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
	deadline := time.Now().Add(2 * time.Second)
	for f.server.active.Load() == maxConnections && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
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
	select {
	case err := <-f.done:
		f.done <- err // The fixture cleanup also observes the Serve result.
		if err != nil {
			t.Fatalf("shutdown service: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not finish shutdown")
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
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("unexpected listener failure was lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener failure waited for a verified client to disconnect")
	}
	if _, err := client.Do(probe); err == nil {
		t.Fatal("verified connection survived listener failure")
	}
}

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func TestPolicyRestartClosesAlreadyVerifiedConnections(t *testing.T) {
	f := newFixture(t)
	listener, err := net.Listen("unix", f.socket)
	testutil.FailErr(t, "listen", err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(ctx, listener) }()
	conn, err := net.Dial("unix", f.socket)
	testutil.FailErr(t, "connect", err)
	defer func() { _ = conn.Close() }()
	testutil.FailErr(t, "probe", vaultidentity.WriteFrame(conn, vaultidentity.Request{Op: vaultidentity.OpProbe}))
	var response vaultidentity.Response
	testutil.FailErr(t, "read probe", vaultidentity.ReadFrame(conn, &response))
	if response.Status != vaultidentity.StatusOK {
		t.Fatalf("caller was not verified: %+v", response)
	}
	cancel()
	select {
	case err := <-done:
		testutil.FailErr(t, "shutdown verified service", err)
	case <-time.After(2 * time.Second):
		t.Fatal("service cancellation retained a verified connection")
	}
	if err := vaultidentity.ReadFrame(conn, &response); err == nil {
		t.Fatal("connection remained usable after policy restart")
	}
}

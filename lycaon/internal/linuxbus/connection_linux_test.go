//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionCancellationBoundsSilentAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on authentication fixture: %v", err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		<-stop
	}()
	t.Cleanup(func() { close(stop); _ = listener.Close(); <-done })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if bus, err := Connect(ctx, path); !errors.Is(err, context.DeadlineExceeded) || bus != nil {
		t.Fatalf("silent connection = %v, %v", bus, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("authentication cancellation took %s", elapsed)
	}
}

func TestConnectionRefusesMissingSocketsAndCanceledCallers(t *testing.T) {
	if bus, err := Connect(t.Context(), filepath.Join(t.TempDir(), "missing")); err == nil || bus != nil {
		t.Fatalf("missing socket connection = %v, %v", bus, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if bus, err := Connect(ctx, filepath.Join(t.TempDir(), "missing")); !errors.Is(err, context.Canceled) || bus != nil {
		t.Fatalf("canceled connection = %v, %v", bus, err)
	}
}

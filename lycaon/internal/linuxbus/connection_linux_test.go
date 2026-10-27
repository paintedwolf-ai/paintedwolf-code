//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionCancellationBoundsSilentAuthentication(t *testing.T) {
	directory, err := os.MkdirTemp(os.TempDir(), "pwb-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	if err != nil {
		t.Fatalf("create socket fixture directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "bus")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on authentication fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stop, done := make(chan struct{}), make(chan struct{})
	authRead := make(chan error, 1)
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			authRead <- err
			return
		}
		defer func() { _ = connection.Close() }()
		_, err = connection.Read(make([]byte, 1))
		authRead <- err
		cancel()
		<-stop
	}()
	t.Cleanup(func() { close(stop); _ = listener.Close(); <-done })
	if bus, err := Connect(ctx, path); bus != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("silent connection = %v, %v", bus, err)
	}
	if err := <-authRead; err != nil {
		t.Fatalf("read authentication attempt: %v", err)
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

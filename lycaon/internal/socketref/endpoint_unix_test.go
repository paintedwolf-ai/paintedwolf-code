//go:build darwin || linux

package socketref

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func endpointFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pw-e-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create short endpoint fixture directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func endpointClient(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Net: "unix", Name: path})
	testutil.FailErr(t, "connect to service endpoint", err)
	t.Cleanup(func() { _ = conn.Close() })
	testutil.FailErr(t, "bound fixture wait", conn.SetDeadline(time.Now().Add(5*time.Second)))
	return conn
}

func assertEndpointReleased(t *testing.T, endpoint *unixEndpoint) {
	t.Helper()
	testutil.FailErr(t, "close service endpoint", endpoint.Close())
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if len(endpoint.connections) != 0 || len(endpoint.slots) != 0 {
		t.Fatalf("endpoint retained resources: connections=%d, slots=%d", len(endpoint.connections), len(endpoint.slots))
	}
	if _, err := os.Lstat(endpoint.Path()); !os.IsNotExist(err) {
		t.Fatalf("released endpoint still has a pathname: %v", err)
	}
}

func TestEndpointAdmissionBoundAndCancellationJoin(t *testing.T) {
	entered := make(chan struct{}, endpointConnections)
	var dials atomic.Int32
	endpoint, err := startEndpoint(filepath.Join(endpointFixtureRoot(t), "relay"), func(ctx context.Context) (net.Conn, error) {
		dials.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	testutil.FailErr(t, "start bounded endpoint", err)
	t.Cleanup(func() { _ = endpoint.Close() })
	for range endpointConnections {
		endpointClient(t, endpoint.Path())
		select {
		case <-entered:
		case <-t.Context().Done():
			t.Fatal("fixture ended before endpoint admission")
		}
	}
	extra := endpointClient(t, endpoint.Path())
	var one [1]byte
	if n, err := extra.Read(one[:]); n != 0 || err != io.EOF {
		t.Fatalf("excess connection was not refused: bytes=%d, error=%v", n, err)
	}
	if got := dials.Load(); got != endpointConnections {
		t.Fatalf("dial work exceeded admission: got=%d, limit=%d", got, endpointConnections)
	}
	assertEndpointReleased(t, endpoint)
}

func TestEndpointPreservesHalfCloseAndResponse(t *testing.T) {
	root := endpointFixtureRoot(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: filepath.Join(root, "service")})
	testutil.FailErr(t, "listen for half-close fixture", err)
	defer listener.Close()
	finished := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			finished <- err
			return
		}
		defer conn.Close()
		data, err := io.ReadAll(conn)
		if err == nil {
			_, err = conn.Write(append([]byte("reply:"), data...))
		}
		finished <- err
	}()
	endpoint, err := startEndpoint(filepath.Join(root, "relay"), func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", listener.Addr().String())
	})
	testutil.FailErr(t, "start half-close endpoint", err)
	defer endpoint.Close()
	client := endpointClient(t, endpoint.Path())
	_, err = client.Write([]byte("request"))
	testutil.FailErr(t, "write endpoint request", err)
	testutil.FailErr(t, "half-close endpoint request", client.CloseWrite())
	response, err := io.ReadAll(client)
	testutil.FailErr(t, "read endpoint response after half-close", err)
	if string(response) != "reply:request" {
		t.Fatalf("response changed: %q", response)
	}
	testutil.FailErr(t, "serve endpoint response", <-finished)
	assertEndpointReleased(t, endpoint)
}

func receiveEndpointFD(conn *net.UnixConn) (int, error) {
	var data [1]byte
	control := make([]byte, unix.CmsgSpace(4))
	n, _, fds, err := readUnixMessage(conn, data[:], control)
	if err != nil || n != 1 || len(fds) != 1 {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		return -1, io.ErrUnexpectedEOF
	}
	return fds[0], nil
}

func TestEndpointPreservesDescriptorCustodyInBothDirections(t *testing.T) {
	root := endpointFixtureRoot(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: filepath.Join(root, "service")})
	testutil.FailErr(t, "listen for descriptor fixture", err)
	defer listener.Close()
	endpoint, err := startEndpoint(filepath.Join(root, "relay"), func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", listener.Addr().String())
	})
	testutil.FailErr(t, "start descriptor endpoint", err)
	defer endpoint.Close()
	client := endpointClient(t, endpoint.Path())
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create descriptor custody pipe", err)
	defer reader.Close()
	defer writer.Close()
	finished := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			finished <- err
			return
		}
		defer conn.Close()
		fd, err := receiveEndpointFD(conn)
		if err != nil {
			finished <- err
			return
		}
		defer unix.Close(fd)
		finished <- writeUnixMessage(conn, []byte("r"), unix.UnixRights(fd))
	}()
	testutil.FailErr(t, "send descriptor through endpoint", writeUnixMessage(client, []byte("s"), unix.UnixRights(int(writer.Fd()))))
	returned, err := receiveEndpointFD(client)
	testutil.FailErr(t, "receive returned descriptor", err)
	flags, err := unix.FcntlInt(uintptr(returned), unix.F_GETFD, 0)
	testutil.FailErr(t, "inspect returned descriptor custody", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("received descriptor can leak into another launch")
	}
	_, err = unix.Write(returned, []byte("round trip"))
	testutil.FailErr(t, "write through returned descriptor", err)
	testutil.FailErr(t, "release returned descriptor", unix.Close(returned))
	testutil.FailErr(t, "release original descriptor", writer.Close())
	testutil.FailErr(t, "finish descriptor service", <-finished)
	testutil.FailErr(t, "finish descriptor request", client.Close())
	assertEndpointReleased(t, endpoint)
	testutil.FailErr(t, "bound descriptor custody observation", reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	payload, err := io.ReadAll(reader)
	testutil.FailErr(t, "observe all forwarded descriptor references released", err)
	if string(payload) != "round trip" {
		t.Fatalf("descriptor payload changed: %q", payload)
	}
}

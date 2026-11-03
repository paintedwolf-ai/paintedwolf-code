//go:build darwin || linux

package socketref

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func datagramFixture(t *testing.T) (*net.UnixConn, *datagramEndpoint, *atomic.Int32, string) {
	t.Helper()
	root := endpointFixtureRoot(t)
	service, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Net: "unixgram", Name: filepath.Join(root, "service")})
	testutil.FailErr(t, "listen for datagram fixture", err)
	t.Cleanup(func() { _ = service.Close() })
	testutil.FailErr(t, "bound datagram service fixture wait", service.SetDeadline(time.Now().Add(10*time.Second)))
	count := &atomic.Int32{}
	endpoint, err := startDatagramEndpoint(filepath.Join(root, "relay"), func(ctx context.Context, local string) (net.Conn, error) {
		count.Add(1)
		dialer := net.Dialer{LocalAddr: &net.UnixAddr{Net: "unixgram", Name: local}}
		return dialer.DialContext(ctx, "unixgram", service.LocalAddr().String())
	})
	testutil.FailErr(t, "start datagram endpoint", err)
	t.Cleanup(func() { _ = endpoint.Close() })
	return service, endpoint, count, root
}

func datagramClient(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	client, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Net: "unixgram", Name: path})
	testutil.FailErr(t, "bind datagram fixture client", err)
	t.Cleanup(func() { _ = client.Close() })
	testutil.FailErr(t, "bound datagram client fixture wait", client.SetDeadline(time.Now().Add(10*time.Second)))
	return client
}

func assertDatagramEndpointReleased(t *testing.T, endpoint *datagramEndpoint, root string) {
	t.Helper()
	testutil.FailErr(t, "close datagram endpoint", endpoint.Close())
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if len(endpoint.peers) != 0 {
		t.Fatalf("released endpoint retained %d peers", len(endpoint.peers))
	}
	if _, err := os.Lstat(endpoint.Path()); !os.IsNotExist(err) {
		t.Fatalf("released datagram endpoint still exists: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "peer-*"))
	testutil.FailErr(t, "inspect released association addresses", err)
	if len(paths) != 0 {
		t.Fatalf("released endpoint retained addresses: %v", paths)
	}
}

func TestDatagramEndpointPreservesEmptyMessagesAndClientRouting(t *testing.T) {
	service, endpoint, _, root := datagramFixture(t)
	destination := &net.UnixAddr{Net: "unixgram", Name: endpoint.Path()}
	for _, name := range []string{"first", "second", "client-é"} {
		client := datagramClient(t, filepath.Join(root, name))
		for _, payload := range []string{"one", "", "two"} {
			testutil.FailErr(t, "send complete fixture datagram", writeUnixDatagram(client, []byte(payload), nil, destination))
			var data [32]byte
			n, _, _, sender, err := service.ReadMsgUnix(data[:], nil)
			testutil.FailErr(t, "receive retained datagram", err)
			if string(data[:n]) != payload {
				t.Fatalf("datagram boundary changed: got=%q, want=%q", data[:n], payload)
			}
			testutil.FailErr(t, "reply to retained datagram association", writeUnixDatagram(service, data[:n], nil, sender))
			n, _, _, _, err = client.ReadMsgUnix(data[:], nil)
			testutil.FailErr(t, "receive own datagram reply", err)
			if string(data[:n]) != payload {
				t.Fatalf("datagram reply changed: got=%q, want=%q", data[:n], payload)
			}
		}
	}
	assertDatagramEndpointReleased(t, endpoint, root)
}

func TestDatagramEndpointBoundsAssociationsBeforeDialAndJoinsThem(t *testing.T) {
	service, endpoint, dials, root := datagramFixture(t)
	destination := &net.UnixAddr{Net: "unixgram", Name: endpoint.Path()}
	var first *net.UnixConn
	for i := range endpointConnections {
		client := datagramClient(t, filepath.Join(root, string(rune('a'+i))))
		if first == nil {
			first = client
		}
		testutil.FailErr(t, "admit bounded datagram association", writeUnixDatagram(client, []byte("open"), nil, destination))
		var data [32]byte
		_, err := service.Read(data[:])
		testutil.FailErr(t, "observe admitted datagram association", err)
	}
	extra := datagramClient(t, filepath.Join(root, "excess"))
	testutil.FailErr(t, "submit excess datagram association", writeUnixDatagram(extra, []byte("excess"), nil, destination))
	testutil.FailErr(t, "submit admitted ordering barrier", writeUnixDatagram(first, []byte("barrier"), nil, destination))
	var data [32]byte
	n, err := service.Read(data[:])
	testutil.FailErr(t, "observe admitted ordering barrier", err)
	if string(data[:n]) != "barrier" || dials.Load() != endpointConnections {
		t.Fatalf("excess association crossed admission: payload=%q, dials=%d", data[:n], dials.Load())
	}
	assertDatagramEndpointReleased(t, endpoint, root)
}

func TestDatagramEndpointSupportsUnnamedSenders(t *testing.T) {
	service, endpoint, _, root := datagramFixture(t)
	client, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Net: "unixgram", Name: endpoint.Path()})
	testutil.FailErr(t, "connect unnamed datagram client", err)
	defer client.Close()
	testutil.FailErr(t, "send unnamed client's datagram", writeUnixDatagram(client, []byte("one-way"), nil, nil))
	var data [32]byte
	n, err := service.Read(data[:])
	testutil.FailErr(t, "receive unnamed client's datagram", err)
	if string(data[:n]) != "one-way" {
		t.Fatalf("unnamed sender payload=%q", data[:n])
	}
	assertDatagramEndpointReleased(t, endpoint, root)
}

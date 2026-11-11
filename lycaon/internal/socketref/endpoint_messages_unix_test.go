//go:build darwin || linux

package socketref

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestReceivedDescriptorsHaveAggregateCustodyAndReleaseRejectedRights(t *testing.T) {
	root := endpointFixtureRoot(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: filepath.Join(root, "service")})
	testutil.FailErr(t, "listen for descriptor admission fixture", err)
	defer listener.Close()
	client := endpointClient(t, listener.Addr().String())
	service, err := listener.AcceptUnix()
	testutil.FailErr(t, "accept descriptor admission fixture", err)
	defer service.Close()
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create descriptor admission custody witness", err)
	defer reader.Close()
	defer writer.Close()
	budget := descriptorCustody{limit: 4}
	var data [1]byte
	control := make([]byte, unix.CmsgSpace(5*4))
	testutil.FailErr(t, "send admitted descriptor set", writeUnixMessage(client, []byte("a"), unix.UnixRights(int(writer.Fd()), int(writer.Fd()), int(writer.Fd()))))
	_, _, held, _, err := budget.read(service, data[:], control)
	testutil.FailErr(t, "retain admitted descriptor set", err)
	defer func() { budget.release(held) }()
	if len(held) != 3 || budget.retained != 3 {
		t.Fatalf("descriptor custody=%d, retained=%d", len(held), budget.retained)
	}
	testutil.FailErr(t, "send excess descriptor set", writeUnixMessage(client, []byte("b"), unix.UnixRights(int(writer.Fd()), int(writer.Fd()))))
	_, _, refused, _, err := budget.read(service, data[:], control)
	if err == nil || len(refused) != 0 || budget.retained != 3 {
		t.Fatalf("excess descriptors were retained: fds=%v, count=%d, error=%v", refused, budget.retained, err)
	}
	_, err = unix.Write(held[0], []byte("retained"))
	testutil.FailErr(t, "use admitted descriptor after rejecting excess", err)
	budget.release(held)
	held = nil
	testutil.FailErr(t, "release original descriptor witness", writer.Close())
	testutil.FailErr(t, "bound descriptor leak observation", reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	result, err := io.ReadAll(reader)
	testutil.FailErr(t, "observe final descriptor custody release", err)
	if string(result) != "retained" || budget.retained != 0 {
		t.Fatalf("descriptor ownership changed: payload=%q, count=%d", result, budget.retained)
	}
}

func TestDatagramEndpointPreservesDescriptorsWithoutInsertingPayload(t *testing.T) {
	service, endpoint, _, root := datagramFixture(t)
	client := datagramClient(t, filepath.Join(root, "client"))
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create datagram descriptor witness", err)
	defer reader.Close()
	defer writer.Close()
	destination := &net.UnixAddr{Net: "unixgram", Name: endpoint.Path()}
	testutil.FailErr(t, "send empty descriptor datagram", writeUnixDatagram(client, nil, unix.UnixRights(int(writer.Fd())), destination))
	var data [1]byte
	control := make([]byte, unix.CmsgSpace(4))
	n, oobn, fds, sender, err := endpointDescriptors.read(service, data[:], control)
	testutil.FailErr(t, "read forwarded empty descriptor datagram", err)
	defer func() { endpointDescriptors.release(fds) }()
	if n != 0 || len(fds) != 1 || sender == nil {
		t.Fatalf("empty descriptor datagram changed: bytes=%d, descriptors=%d, sender=%v", n, len(fds), sender)
	}
	testutil.FailErr(t, "return empty descriptor datagram", writeUnixDatagram(service, nil, control[:oobn], &net.UnixAddr{Net: "unixgram", Name: sender.Name}))
	n, _, returned, _, err := endpointDescriptors.read(client, data[:], control)
	testutil.FailErr(t, "receive empty descriptor datagram response", err)
	defer func() { endpointDescriptors.release(returned) }()
	if n != 0 || len(returned) != 1 {
		t.Fatalf("empty descriptor reply changed: bytes=%d, descriptors=%d", n, len(returned))
	}
	endpointDescriptors.release(returned)
	returned = nil
	endpointDescriptors.release(fds)
	fds = nil
	testutil.FailErr(t, "release original datagram descriptor", writer.Close())
	assertDatagramEndpointReleased(t, endpoint, root)
	testutil.FailErr(t, "bound datagram descriptor custody observation", reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadAll(reader)
	testutil.FailErr(t, "observe all datagram descriptor copies released", err)
}

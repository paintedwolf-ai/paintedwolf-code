package sessionipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryTiesEndpointsToTheirDirectoryAndSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pw-reg-") //nolint:usetesting // Unix socket addresses have a fixed size.
	testutil.FailErr(t, "create short socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	var reg Registry
	channel, err := reg.Channel(root)
	testutil.FailErr(t, "open session channel", err)
	defer channel.Close()
	name := filepath.Join(root, "endpoint")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	testutil.FailErr(t, "bind endpoint", err)
	defer listener.Close()
	fd, err := unix.Open(name, unix.O_PATH|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "pin endpoint", err)
	defer unix.Close(fd)
	if Admits(int(channel.Fd()), fd) || Register(int(channel.Fd()), fd) || ask(int(channel.Fd()), fd, 0) {
		t.Fatal("an unregistered pathname was admitted")
	}
	linked := filepath.Join(root, "alias")
	testutil.FailErr(t, "link session root", os.Symlink(root, linked))
	if file, err := reg.Channel(linked); err == nil {
		file.Close()
		t.Fatal("accepted a linked session root")
	}
}

func TestRegistryRetainsTheBoundObjectUntilLaunchExit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pw-held-") //nolint:usetesting // Unix socket addresses have a fixed size.
	testutil.FailErr(t, "create short socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir, err := unix.Open(root, unix.O_DIRECTORY|unix.O_PATH|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "pin scratch directory", err)
	defer unix.Close(dir)
	session, err := identify(dir, unix.S_IFDIR)
	testutil.FailErr(t, "identify scratch directory", err)
	path := filepath.Join(root, "service")
	listen := func() *net.UnixListener {
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		testutil.FailErr(t, "bind listener", err)
		ln.SetUnlinkOnClose(false)
		return ln
	}
	pin := func() int {
		fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		testutil.FailErr(t, "pin socket", err)
		return fd
	}
	original := listen()
	defer original.Close()
	var reg Registry
	owned := make(map[object]int)
	if !reg.retain(session, owned, pin()) {
		t.Fatal("socket registration failed")
	}
	client, err := reg.Channel(root)
	testutil.FailErr(t, "open client", err)
	defer client.Close()
	before := pin()
	defer unix.Close(before)
	if !Admits(int(client.Fd()), before) {
		t.Fatal("creating session lost the endpoint")
	}
	other, err := reg.Channel(t.TempDir())
	testutil.FailErr(t, "open unrelated client", err)
	defer other.Close()
	if Admits(int(other.Fd()), before) {
		t.Fatal("another session received the endpoint")
	}
	testutil.FailErr(t, "retire old socket path", os.Remove(path))
	otherListener := listen()
	defer otherListener.Close()
	after := pin()
	defer unix.Close(after)
	if Admits(int(client.Fd()), after) {
		t.Fatal("replacement endpoint was admitted")
	}
	reg.release(session, owned)
	if Admits(int(client.Fd()), before) {
		t.Fatal("closed launch retained authority")
	}
}

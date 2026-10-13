//go:build linux

package hostpower

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type fixtureLogind struct {
	mu        sync.Mutex
	writer    *os.File
	arguments []string
}

func (f *fixtureLogind) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.arguments = []string{what, who, why, mode}
	return dbus.UnixFD(f.writer.Fd()), nil
}

func fixtureSocketDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(os.TempDir(), "pwb-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	testutil.FailErr(t, "create short socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func fixtureBus(t *testing.T) string {
	t.Helper()
	path := filepath.Join(fixtureSocketDirectory(t), "bus")
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--address=unix:path="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start isolated bus", cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		time.Sleep(time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("isolated bus did not start: %s", stderr.String())
	return ""
}

func TestLogindLeaseTransfersDescriptorAndPreservesUserSuspend(t *testing.T) {
	path := fixtureBus(t)
	server, err := connectLogindBus(path)
	testutil.FailErr(t, "connect fixture service", err)
	defer func() { _ = server.Close() }()
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create inhibitor pipe", err)
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	fixture := &fixtureLogind{writer: writer}
	testutil.FailErr(t, "export fixture service", server.Export(fixture, logindPath, logindName+".Manager"))
	result, err := server.RequestName(logindName, dbus.NameFlagDoNotQueue)
	testutil.FailErr(t, "own fixture service name", err)
	if result != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal("fixture service name not owned")
	}
	client, err := connectLogindBus(path)
	testutil.FailErr(t, "connect inhibitor client", err)
	defer func() { _ = client.Close() }()
	held, err := acquireLogindLease(client)
	testutil.FailErr(t, "acquire native descriptor lease", err)
	defer func() { _ = held.Release() }()
	fixture.mu.Lock()
	args := append([]string(nil), fixture.arguments...)
	fixture.mu.Unlock()
	if len(args) != 4 || args[0] != "idle" || args[1] != "Painted Wolf Code" || args[3] != "block" {
		t.Fatalf("inhibitor arguments = %v", args)
	}
	flags, err := unix.FcntlInt(held.(*logindLease).file.Fd(), unix.F_GETFD, 0)
	testutil.FailErr(t, "inspect descriptor inheritance", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("inhibitor descriptor could reach a child process")
	}
	testutil.FailErr(t, "close service descriptor copy", writer.Close())
	ended := make(chan error, 1)
	go func() { var data [1]byte; _, err := reader.Read(data[:]); ended <- err }()
	select {
	case err := <-ended:
		t.Fatalf("lease ended before release: %v", err)
	default:
	}
	testutil.FailErr(t, "release inhibitor", held.Release())
	testutil.FailErr(t, "repeat release", held.Release())
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("released inhibitor pipe = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release left an inhibitor descriptor open")
	}
}

func TestLogindLeaseReportsServiceLossAndClosesItsDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create service lifetime", err)
	held := &logindLease{file: writer, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	testutil.FailErr(t, "end service lifetime", reader.Close())
	select {
	case err := <-held.Done():
		if err == nil {
			t.Fatal("lost inhibitor had no failure")
		}
	case <-time.After(time.Second):
		t.Fatal("service loss was not detected")
	}
	testutil.FailErr(t, "release lost inhibitor", held.Release())
	if _, err := writer.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("lost descriptor still open: %v", err)
	}
}

func TestMissingLogindDoesNotAdvertiseOrAcquireAnInhibitor(t *testing.T) {
	client, err := connectLogindBus(fixtureBus(t))
	testutil.FailErr(t, "connect bus without logind", err)
	defer func() { _ = client.Close() }()
	if logindAvailable(client) {
		t.Fatal("missing service advertised support")
	}
	if held, err := acquireLogindLease(client); held != nil || err == nil {
		t.Fatalf("missing service lease=%v error=%v", held, err)
	}
}

func TestLogindAuthenticationCannotHangHostStartup(t *testing.T) {
	path := filepath.Join(fixtureSocketDirectory(t), "bus")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen as silent bus", err)
	defer func() { _ = listener.Close() }()
	accepted := make(chan *net.UnixConn, 1)
	go func() { connection, _ := listener.AcceptUnix(); accepted <- connection }()
	started := time.Now()
	if bus, err := connectLogindBus(path); bus != nil || err == nil {
		t.Fatal("silent bus authenticated")
	}
	if time.Since(started) > 2*logindDeadline {
		t.Fatal("silent bus exceeded the authentication deadline")
	}
	connection := <-accepted
	if connection != nil {
		_ = connection.Close()
	}
}

//go:build darwin

package socketref

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const peerFixtureEnv = "PW_SOCKETREF_PEER_FIXTURE"

func TestSocketPeerFixture(t *testing.T) {
	path := os.Getenv(peerFixtureEnv)
	if path == "" {
		return
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	testutil.FailErr(t, "listen in peer process fixture", err)
	ready := os.NewFile(3, "peer-fixture-ready")
	_, err = ready.Write([]byte{1})
	testutil.FailErr(t, "announce peer fixture readiness", err)
	_ = ready.Close()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		go func() { _, _ = fmt.Fprintln(conn, os.Getpid()); _, _ = io.Copy(conn, conn); _ = conn.Close() }()
	}
}

func peerFixture(t *testing.T, path string) int {
	t.Helper()
	self, err := os.Executable()
	testutil.FailErr(t, "locate peer fixture image", err)
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create peer fixture readiness pipe", err)
	defer reader.Close()
	cmd := exec.CommandContext(t.Context(), self, "-test.run=^TestSocketPeerFixture$")
	cmd.Env = append(os.Environ(), peerFixtureEnv+"="+path)
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Stderr = os.Stderr
	testutil.FailErr(t, "start peer fixture process", cmd.Start())
	_ = writer.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var signal [1]byte
	_, err = io.ReadFull(reader, signal[:])
	testutil.FailErr(t, "observe peer fixture readiness", err)
	return cmd.Process.Pid
}

func TestPeerBindingRejectsAnotherInstanceOfTheSameExecutable(t *testing.T) {
	root := endpointFixtureRoot(t)
	first, replacement := filepath.Join(root, "first"), filepath.Join(root, "replacement")
	peerFixture(t, first)
	peerFixture(t, replacement)
	verify := newPeerBinding()
	for range 3 {
		conn := endpointClient(t, first)
		testutil.FailErr(t, "verify original process instance", verify(conn))
	}
	other := endpointClient(t, replacement)
	if err := verify(other); !errors.Is(err, ErrUnbound) {
		t.Fatalf("replacement process inherited approval: %v", err)
	}
}

func TestRetainedEndpointCannotFollowPathReplacement(t *testing.T) {
	root := endpointFixtureRoot(t)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path, decoy := filepath.Join(root, "service"), filepath.Join(root, "decoy")
	firstPID := peerFixture(t, path)
	peerFixture(t, decoy)
	owner, err := Capture(path)
	testutil.FailErr(t, "capture original service", err)
	defer owner.Close()
	testutil.FailErr(t, "rename original service", os.Rename(path, filepath.Join(root, "original")))
	testutil.FailErr(t, "replace original service name", os.Rename(decoy, path))
	endpoint, err := owner.Endpoint("unix")
	testutil.FailErr(t, "bind retained service endpoint", err)
	conn := endpointClient(t, endpoint)
	response := bufio.NewReader(conn)
	identity, err := response.ReadString('\n')
	testutil.FailErr(t, "observe retained recipient process", err)
	if strings.TrimSpace(identity) != strconv.Itoa(firstPID) {
		t.Fatalf("endpoint reached a replacement process: %q", identity)
	}
	_, err = conn.Write([]byte("original service"))
	testutil.FailErr(t, "write to retained endpoint", err)
	got := make([]byte, len("original service"))
	_, err = io.ReadFull(response, got)
	testutil.FailErr(t, "read retained endpoint echo", err)
	if string(got) != "original service" {
		t.Fatalf("retained service payload changed: %q", got)
	}
	ref, release, err := owner.operation()
	testutil.FailErr(t, "inspect retained endpoint", err)
	// The alias still identifies the original inode; the requested path does not.
	if matchesPath(ref, path) {
		t.Fatal("replacement pathname still matches retained authority")
	}
	release()
	owner.Close()
	if _, err := net.Dial("unix", endpoint); err == nil {
		t.Fatal("released endpoint accepted a connection")
	}
}

func TestRetainedEndpointHandlesLongStatePaths(t *testing.T) {
	root := endpointFixtureRoot(t)
	state := filepath.Join(t.TempDir(), strings.Repeat("long-state-", 18))
	t.Setenv("LYCAON_CONFIG_DIR", state)
	path := filepath.Join(root, "service")
	pid := peerFixture(t, path)
	owner, err := Capture(path)
	testutil.FailErr(t, "capture service with long state path", err)
	defer owner.Close()
	endpoint, err := owner.Endpoint("unix")
	testutil.FailErr(t, "create short service address", err)
	if len(endpoint) >= 104 {
		t.Fatalf("service address exceeds kernel bound: %d", len(endpoint))
	}
	protected, err := filepath.EvalSymlinks(endpoint)
	testutil.FailErr(t, "resolve protected service endpoint", err)
	state, err = filepath.EvalSymlinks(state)
	testutil.FailErr(t, "resolve fixture state directory", err)
	if len(protected) < 104 || !strings.HasPrefix(protected, state+string(filepath.Separator)) {
		t.Fatalf("fixture did not retain a long protected address: %q", protected)
	}
	conn := endpointClient(t, endpoint)
	identity, err := bufio.NewReader(conn).ReadString('\n')
	testutil.FailErr(t, "connect through short address", err)
	if strings.TrimSpace(identity) != strconv.Itoa(pid) {
		t.Fatalf("short address changed service recipient: %q", identity)
	}
	owner.Close()
	if _, err := os.Lstat(filepath.Dir(filepath.Dir(endpoint))); !os.IsNotExist(err) {
		t.Fatalf("short address namespace survived release: %v", err)
	}
}

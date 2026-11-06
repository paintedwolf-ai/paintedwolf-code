package lineage_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOwnedSocketFixtureChild(t *testing.T) {
	if os.Getenv("PW_OWNED_SOCKET_FIXTURE") != "1" {
		return
	}
	original := os.NewFile(4, "fixture-listener")
	listener, err := net.FileListener(original)
	_ = original.Close()
	if err != nil {
		testutil.FailErr(t, "recover inherited listener", err)
	}
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "close":
			if err := listener.Close(); err != nil {
				testutil.FailErr(t, "close owned child listener", err)
			}
			fmt.Println("closed")
		case "exit":
			return
		}
	}
}

func TestOwnedSocketsRequiresCurrentListenerNotRequestedOrClosedPort(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("native observation is unavailable")
	}
	l, err := lineage.Open("synthetic-action", "synthetic-session")
	if err != nil {
		testutil.FailErr(t, "open owned lineage fixture", err)
	}
	defer l.Close()
	listener, sibling := listenOnBothLoopbacks(t)
	defer listener.Close()
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	inherited, err := listener.File()
	if err != nil {
		testutil.FailErr(t, "duplicate owned listener descriptor", err)
	}
	defer inherited.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedSocketFixtureChild$")
	cmd.Env = append(os.Environ(), "PW_OWNED_SOCKET_FIXTURE=1")
	cmd.ExtraFiles = []*os.File{l.ChildFile(), inherited}
	if sibling != nil {
		defer sibling.Close()
		siblingFile, err := sibling.File()
		if err != nil {
			testutil.FailErr(t, "duplicate owned sibling descriptor", err)
		}
		defer siblingFile.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, siblingFile)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		testutil.FailErr(t, "open child control input", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		testutil.FailErr(t, "open child readiness output", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		testutil.FailErr(t, "start owned listener child", err)
	}
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	l.Started()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child did not establish real listener")
	}
	_ = listener.Close()
	_ = inherited.Close()
	if sibling != nil {
		_ = sibling.Close()
	}
	sockets, err := lineage.OwnedSockets(context.Background(), l.ID())
	if err != nil {
		testutil.FailErr(t, "observe native owned socket holders", err)
	}
	seen := false
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint && socket.Holder.PID == cmd.Process.Pid && socket.ResourceID != "" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("actual inherited listener was not observed")
	}
	identity, paired, err := lineage.ObserveSessionListener(t.Context(), "synthetic-session", endpoint)
	if err != nil || !paired || identity == "" {
		t.Fatalf("native observation did not identify the owned listener: paired=%v, err=%v", paired, err)
	}
	if _, err := fmt.Fprintln(input, "close"); err != nil {
		testutil.FailErr(t, "request owned listener closure", err)
	}
	if !scanner.Scan() || scanner.Text() != "closed" {
		t.Fatal("child did not close listener")
	}
	foreign, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		testutil.FailErr(t, "bind foreign replacement listener", err)
	}
	defer foreign.Close()
	sockets, err = lineage.OwnedSockets(context.Background(), l.ID())
	if err != nil {
		testutil.FailErr(t, "observe sockets after foreign replacement", err)
	}
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint {
			t.Fatal("closed owned port attributed a foreign replacement")
		}
	}
	if _, paired, err := lineage.ObserveSessionListener(t.Context(), "synthetic-session", endpoint); paired || err != nil {
		t.Fatalf("a foreign replacement on the endpoint: paired=%v, err=%v", paired, err)
	}
}

// listenOnBothLoopbacks takes one port on both loopback addresses, so a listener
// another test binds to the other family cannot decide this one's ownership. The
// sibling is nil where this host has no IPv6 loopback.
func listenOnBothLoopbacks(t *testing.T) (*net.TCPListener, *net.TCPListener) {
	t.Helper()
	for range 32 {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			testutil.FailErr(t, "listen on owned loopback socket", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		sibling, err := net.ListenTCP("tcp6", &net.TCPAddr{IP: net.IPv6loopback, Port: port})
		switch {
		case err == nil:
			return listener, sibling
		case errors.Is(err, syscall.EADDRINUSE):
			_ = listener.Close()
		default:
			return listener, nil
		}
	}
	t.Fatal("no port is free on both loopback addresses")
	return nil, nil
}

func TestUnknownSessionCannotUseARequestedListenerAsOwnership(t *testing.T) {
	identity, owned, err := lineage.ObserveSessionListener(t.Context(), "unrelated-fixture-session", netip.MustParseAddrPort("127.0.0.1:49151"))
	if !lineage.Supported() {
		if !errors.Is(err, lineage.ErrUnsupported) {
			t.Fatal("missing unsupported capability")
		}
		return
	}
	if err != nil || owned || identity != "" {
		t.Fatalf("requested port became ownership: owned=%v err=%v", owned, err)
	}
}

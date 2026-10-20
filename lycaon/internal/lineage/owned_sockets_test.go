package lineage_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
)

func TestOwnedSocketFixtureChild(t *testing.T) {
	if os.Getenv("PW_OWNED_SOCKET_FIXTURE") != "1" {
		return
	}
	original := os.NewFile(4, "fixture-listener")
	listener, err := net.FileListener(original)
	_ = original.Close()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "close":
			if err := listener.Close(); err != nil {
				t.Fatal(err)
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
		t.Fatal(err)
	}
	defer l.Close()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	inherited, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer inherited.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedSocketFixtureChild$")
	cmd.Env = append(os.Environ(), "PW_OWNED_SOCKET_FIXTURE=1")
	cmd.ExtraFiles = []*os.File{l.ChildFile(), inherited}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	l.Started()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child did not establish real listener")
	}
	_ = listener.Close()
	_ = inherited.Close()
	sockets, err := lineage.OwnedSockets(context.Background(), l.ID())
	if err != nil {
		t.Fatal(err)
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
	if _, err := fmt.Fprintln(input, "close"); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "closed" {
		t.Fatal("child did not close listener")
	}
	foreign, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	sockets, err = lineage.OwnedSockets(context.Background(), l.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint {
			t.Fatal("closed owned port attributed a foreign replacement")
		}
	}
}

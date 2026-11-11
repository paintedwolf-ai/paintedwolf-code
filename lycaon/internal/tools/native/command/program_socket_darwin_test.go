//go:build darwin

package command_test

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramSocketDatagramFixture(t *testing.T) {
	if os.Getenv("PW_SOCKET_DATAGRAM_FIXTURE") != "1" {
		return
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Net: "unixgram", Name: os.Getenv("SERVICE_SOCKET")})
	testutil.FailErr(t, "connect confined datagram fixture", err)
	defer conn.Close()
	for _, payload := range []string{"first", "", "last"} {
		_, err := conn.Write([]byte(payload))
		testutil.FailErr(t, "send confined datagram fixture message", err)
	}
	gate, err := os.Open(os.Getenv("PW_SOCKET_TEST_FIFO"))
	testutil.FailErr(t, "open datagram completion gate", err)
	defer gate.Close()
	var done [1]byte
	_, err = io.ReadFull(gate, done[:])
	testutil.FailErr(t, "wait for service to observe fixture messages", err)
}

func TestProgramSocketDatagramsReachTheirRetainedService(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pw-program-dgram-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create datagram launch fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), strings.Repeat("long-state-", 15)))
	path := filepath.Join(root, "service")
	service, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Net: "unixgram", Name: path})
	testutil.FailErr(t, "bind real datagram service", err)
	defer service.Close()
	testutil.FailErr(t, "bound datagram service fixture wait", service.SetDeadline(time.Now().Add(10*time.Second)))
	f := newProgramFixture(t)
	runtime := approvalstate.NewSocketCapabilityRuntime()
	t.Cleanup(runtime.Close)
	f.Runtime.Executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	f.Runtime.Executor.Capabilities.SetApprovalsDisabled(func(string) bool { return true })
	fifo := filepath.Join(f.Dir, "datagram-completion")
	testutil.FailErr(t, "create datagram completion gate", syscall.Mkfifo(fifo, 0600))
	gate, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	testutil.FailErr(t, "hold datagram completion gate", err)
	defer gate.Close()
	finished := make(chan error, 1)
	go func() {
		defer func() { _, _ = gate.Write([]byte{1}) }()
		for _, expected := range []string{"first", "", "last"} {
			var data [32]byte
			n, _, _, _, err := service.ReadMsgUnix(data[:], nil)
			if err != nil {
				finished <- err
				return
			}
			if string(data[:n]) != expected {
				finished <- fmt.Errorf("datagram changed: got=%q, want=%q", data[:n], expected)
				return
			}
		}
		finished <- nil
	}()
	self, err := os.Executable()
	testutil.FailErr(t, "locate datagram launch fixture", err)
	reference := "{{paintedwolf-socket:unixgram:" + url.PathEscape(path) + "}}"
	program := fmt.Sprintf("PW_SOCKET_DATAGRAM_FIXTURE=1 PW_SOCKET_TEST_FIFO=%s SERVICE_SOCKET=%s external -- %s -test.run=^TestProgramSocketDatagramFixture$", strconv.Quote(fifo), strconv.Quote(reference), strconv.Quote(self))
	out, err := f.CallArgs(t, t.Context(), socketProgramArgs(program, path))
	testutil.FailErr(t, "launch real confined datagram client", err)
	testutil.FailErr(t, "observe all service datagrams", <-finished)
	if result := decodeOutcome(t, out); result.Status != 0 {
		t.Fatalf("bound datagram launch failed: %s", out)
	}
}

func TestProgramSocketEndpointSupportsAnEmptyRealSSHAgent(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pw-program-agent-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create isolated SSH agent fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(root, "agent")
	agent := exec.CommandContext(t.Context(), "/usr/bin/ssh-agent", "-D", "-a", path)
	agent.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "LC_ALL=C"}
	testutil.FailErr(t, "start owned empty SSH agent", agent.Start())
	t.Cleanup(func() { _ = agent.Process.Kill(); _ = agent.Wait() })
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("owned SSH agent did not create its fixture socket")
		case <-t.Context().Done():
			t.Fatal("SSH agent fixture canceled before readiness")
		}
	}
	f := newProgramFixture(t)
	runtime := approvalstate.NewSocketCapabilityRuntime()
	t.Cleanup(runtime.Close)
	f.Runtime.Executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	f.Runtime.Executor.Capabilities.SetApprovalsDisabled(func(string) bool { return true })
	reference := "{{paintedwolf-socket:" + url.PathEscape(path) + "}}"
	program := "LC_ALL=C SSH_AUTH_SOCK=" + strconv.Quote(reference) + " external -- /usr/bin/ssh-add -l"
	out, err := f.CallArgs(t, t.Context(), socketProgramArgs(program, path))
	testutil.FailErr(t, "list owned empty SSH agent through prepared launch", err)
	result := decodeOutcome(t, out)
	if result.Status != 1 || !strings.Contains(result.Stdout, "The agent has no identities.") {
		t.Fatalf("explicit SSH_AUTH_SOCK did not preserve agent access: %s", out)
	}
}

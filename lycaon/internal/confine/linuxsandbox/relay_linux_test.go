//go:build linux

package linuxsandbox

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		written   []byte
		wantReady bool
		want      syscall.Signal
	}{
		{"ready then signal", []byte{reportReady, byte(syscall.SIGSYS)}, true, syscall.SIGSYS},
		{"ready only", []byte{reportReady}, true, 0},
		{"nothing written", nil, false, 0},
		{"zero and out of range", []byte{0, 200}, false, 0},
	} {
		ready := make(chan struct{})
		got := readReport(bytes.NewReader(tc.written), ready)
		closed := false
		select {
		case <-ready:
			closed = true
		default:
		}
		if got != tc.want || closed != tc.wantReady {
			t.Fatalf("%s: signal %v ready %v, want %v ready %v", tc.name, got, closed, tc.want, tc.wantReady)
		}
	}
}

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		testutil.FailErr(t, "cmd.Start failed", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

func TestRelayHoldsSignalsUntilReady(t *testing.T) {
	t.Parallel()
	child := startSleeper(t)
	events := make(chan os.Signal, 1)
	ready := make(chan struct{})
	events <- syscall.SIGTERM
	done := make(chan struct{})
	var code int
	var sig syscall.Signal
	var held []syscall.Signal
	go func() {
		code, sig, held = relay(child.Process.Pid, child.Wait, events, ready)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the held signal reached the child before ready")
	case <-time.After(300 * time.Millisecond):
	}
	close(ready)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the signal was not forwarded once ready")
	}
	if sig != syscall.SIGTERM || code != 128+int(syscall.SIGTERM) || len(held) != 0 {
		t.Fatalf("code %d sig %v held %v", code, sig, held)
	}
}

func TestRelayReturnsSignalsTheChildNeverTook(t *testing.T) {
	t.Parallel()
	child := startSleeper(t)
	events := make(chan os.Signal, 1)
	events <- syscall.SIGTERM
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = child.Process.Kill()
	}()
	_, sig, held := relay(child.Process.Pid, child.Wait, events, make(chan struct{}))
	if sig != syscall.SIGKILL || len(held) != 1 || held[0] != syscall.SIGTERM {
		t.Fatalf("sig %v held %v", sig, held)
	}
}

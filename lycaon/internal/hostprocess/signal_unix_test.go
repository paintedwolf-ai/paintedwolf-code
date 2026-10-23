//go:build linux || (darwin && cgo)

package hostprocess

import (
	"syscall"
	"testing"
)

func TestAcceptedSignalsHaveNativeDelivery(t *testing.T) {
	for name, want := range map[string]syscall.Signal{
		"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "INT": syscall.SIGINT,
		"HUP": syscall.SIGHUP, "STOP": syscall.SIGSTOP, "CONT": syscall.SIGCONT,
		"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
	} {
		kind, err := parseSignal(name)
		got, ok := nativeSignal(kind)
		if err != nil || !ok || int(got) != int(want) {
			t.Fatalf("signal %q: native=%d want=%d mapped=%v err=%v", name, got, want, ok, err)
		}
	}
	if _, ok := nativeSignal(0); ok {
		t.Fatal("invalid signal kind has a native delivery")
	}
}

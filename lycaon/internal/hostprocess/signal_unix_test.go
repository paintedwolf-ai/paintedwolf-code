//go:build linux || (darwin && cgo)

package hostprocess

import (
	"errors"
	"os"
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
	if err := signalInstance(Process{}, 0); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("invalid signal kind reached process delivery: %v", err)
	}
}

func TestServiceRefusesUnsupportedSignalForCurrentInstance(t *testing.T) {
	process, err := inspect(os.Getpid())
	if err != nil {
		t.Fatalf("inspect current instance: %v", err)
	}
	if err := (&Service{}).Signal(t.Context(), process, "SIGTERM"); err == nil || errors.Is(err, ErrStale) {
		t.Fatalf("unsupported signal was not refused after identity verification: %v", err)
	}
}

//go:build darwin || linux

package osprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestIncarnationAliveRequiresMatchingStart(t *testing.T) {
	self, ok := IncarnationOf(os.Getpid())
	if !ok {
		t.Fatal("failed to read current process incarnation")
	}
	if !self.Alive() {
		t.Fatal("current process incarnation reported dead")
	}
	for _, invalid := range []Incarnation{
		{},
		{PID: 0, Start: self.Start},
		{PID: -self.PID, Start: self.Start},
		{PID: self.PID, Start: 0},
		{PID: self.PID, Start: -1},
		{PID: self.PID, Start: self.Start + 1},
		{PID: 999999999, Start: self.Start},
	} {
		if invalid.Alive() {
			t.Fatalf("invalid incarnation %+v reported alive", invalid)
		}
	}
}

func TestSignalVerifiesIncarnationBeforeDelivery(t *testing.T) {
	cmd := exec.Command("cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cat: %v", err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	inc, ok := IncarnationOf(cmd.Process.Pid)
	if !ok {
		t.Fatalf("incarnation of %d unavailable", cmd.Process.Pid)
	}

	for _, invalid := range []Incarnation{
		{},
		{PID: 0, Start: inc.Start},
		{PID: inc.PID, Start: 0},
		{PID: inc.PID, Start: inc.Start + 1},
	} {
		if err := Signal(invalid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("Signal(%+v) = %v, want ESRCH", invalid, err)
		}
	}
	if !inc.Alive() {
		t.Fatal("mismatched Signal killed live target")
	}

	if err := Signal(inc, syscall.SIGKILL); err != nil {
		t.Fatalf("Signal(%+v, SIGKILL): %v", inc, err)
	}
	_ = cmd.Wait()

	if inc.Alive() {
		t.Fatalf("reaped incarnation %+v reported alive", inc)
	}
	if err := Signal(inc, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Signal on reaped incarnation = %v, want ESRCH", err)
	}
}

func TestSignalGroupRequiresLiveVerifiedLeader(t *testing.T) {
	cmd := exec.Command("cat")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start group leader: %v", err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	leader, ok := IncarnationOf(cmd.Process.Pid)
	if !ok {
		t.Fatalf("incarnation of leader %d unavailable", cmd.Process.Pid)
	}

	mismatched := Incarnation{PID: leader.PID, Start: leader.Start + 1}
	if err := SignalGroup(mismatched, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SignalGroup(%+v) = %v, want ESRCH", mismatched, err)
	}
	if !leader.Alive() {
		t.Fatal("mismatched SignalGroup killed live leader")
	}

	if err := SignalGroup(leader, syscall.SIGKILL); err != nil {
		t.Fatalf("SignalGroup(%+v, SIGKILL): %v", leader, err)
	}
	_ = cmd.Wait()

	if err := SignalGroup(leader, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SignalGroup on reaped leader = %v, want ESRCH", err)
	}
}

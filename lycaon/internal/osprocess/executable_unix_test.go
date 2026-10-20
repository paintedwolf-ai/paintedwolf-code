//go:build darwin || linux

package osprocess_test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/osprocess"
)

func TestStoppedKernelImageMatchesHeldFileAndRejectsAnotherExecutable(t *testing.T) {
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	other, err := os.Open("/bin/echo")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	child := exec.Command(path, "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if err := child.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var status syscall.WaitStatus
	// WUNTRACED without WCONTINUED admits only a stop or exit. Go
	// classifies the BSD SIGSTOP encoding as Continued; the kernel image
	// adapter independently confirms SSTOP rather than trusting that projection.
	if waited, err := syscall.Wait4(child.Process.Pid, &status, syscall.WUNTRACED, nil); err != nil || waited != child.Process.Pid || status.Exited() || status.Signaled() {
		t.Fatalf("fixture did not stop: status=%v err=%v", status, err)
	}
	matched, err := osprocess.MatchStoppedExecutable(child.Process.Pid, held)
	if err != nil || !matched {
		t.Fatalf("kernel image did not match held executable: matched=%v err=%v", matched, err)
	}
	matched, err = osprocess.MatchStoppedExecutable(child.Process.Pid, other)
	if err != nil || matched {
		t.Fatalf("foreign executable matched: matched=%v err=%v", matched, err)
	}
	if matched, err := osprocess.MatchStoppedExecutable(child.Process.Pid, nil); matched || !errors.Is(err, osprocess.ErrExecutableObservationUnavailable) {
		t.Fatal("missing held identity was accepted")
	}
	if err := child.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if matched, err := osprocess.MatchStoppedExecutable(child.Process.Pid, held); matched || !errors.Is(err, osprocess.ErrExecutableObservationUnavailable) {
		t.Fatal("running process treated as a stopped image")
	}
}

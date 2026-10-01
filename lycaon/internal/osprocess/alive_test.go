package osprocess

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestAliveReportsThisProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process reported as not alive")
	}
}

// On unix a non-positive pid names a process group, and signal 0 reaches the
// caller's own group.
func TestAliveRejectsGroupAndUnusedPIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -os.Getpid(), 999999999} {
		if Alive(pid) {
			t.Fatalf("pid %d reported alive", pid)
		}
	}
}

func TestAliveReportsAnExitedChild(t *testing.T) {
	name, args := "true", []string(nil)
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit"}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for child: %v", err)
	}
	// A reaped child releases its pid, so nothing should answer for it.
	if Alive(pid) {
		t.Fatalf("reaped child %d reported alive", pid)
	}
}

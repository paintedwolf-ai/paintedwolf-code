package osprocess

import (
	"os"
	"os/exec"
	"testing"
)

func TestIncarnationEndsOnlyWithEvidence(t *testing.T) {
	self, ok := CurrentIncarnation(os.Getpid())
	if !ok {
		t.Skip("no process start time on this platform")
	}
	if self.Ended() {
		t.Fatal("this process reads as ended")
	}
	if !(Incarnation{PID: self.PID, Started: self.Started + 1}).Ended() {
		t.Fatal("a reused pid must read as ended for the older incarnation")
	}
	if (Incarnation{PID: self.PID}).Ended() || (Incarnation{Started: 1}).Ended() {
		t.Fatal("an incomplete identity proves nothing")
	}
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	identity, ok := CurrentIncarnation(child.Process.Pid)
	if err := child.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	if ok && !identity.Ended() {
		t.Fatal("a reaped child must read as ended")
	}
}

func TestBootIDIsStableWhenAvailable(t *testing.T) {
	first, ok := BootID()
	if !ok {
		t.Skip("no boot identity on this platform")
	}
	second, _ := BootID()
	if first == "" || first != second {
		t.Fatalf("boot id %q then %q", first, second)
	}
}

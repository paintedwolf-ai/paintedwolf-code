//go:build (darwin && cgo) || linux

package hostprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
)

// startSleep starts a real child; a non-nil lineage makes it that lineage's
// descendant the way a confined launch does.
func startSleep(t *testing.T, line *lineage.Lineage) (*exec.Cmd, chan struct{}) {
	t.Helper()
	child := exec.Command("/bin/sleep", "60")
	if line != nil {
		child.ExtraFiles = []*os.File{line.ChildFile()}
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	if line != nil {
		line.Started()
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	t.Cleanup(func() { _ = child.Process.Kill(); <-done })
	return child, done
}

func TestProcessReferenceAndSignal(t *testing.T) {
	service, err := New()
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	line, err := lineage.Open("command:call_1", "task")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = line.Close() })
	child, done := startSleep(t, line)
	snapshot, err := service.List(t.Context(), "task", child.Process.Pid, 0, 10)
	if err != nil || snapshot.Unavailable != 0 || len(snapshot.Processes) != 1 || !snapshot.Processes[0].Owned || snapshot.Processes[0].Reference == "" {
		t.Fatalf("snapshot child: rows=%v unavailable=%d err=%v", snapshot.Processes, snapshot.Unavailable, err)
	}
	target := snapshot.Processes[0]
	if _, err := service.Resolve("other-task", target.Reference); !errors.Is(err, ErrStale) {
		t.Fatalf("cross-task reference: %v", err)
	}
	if _, err := service.Resolve("task", target.Reference+"x"); !errors.Is(err, ErrStale) {
		t.Fatalf("tampered reference: %v", err)
	}
	restarted, err := New()
	if err != nil {
		t.Fatalf("restart service: %v", err)
	}
	if _, err := restarted.Resolve("task", target.Reference); !errors.Is(err, ErrStale) {
		t.Fatalf("restart reference: %v", err)
	}
	forged := target
	forged.Instance += "0"
	if err := service.Signal(t.Context(), "task", forged, "TERM"); !errors.Is(err, ErrStale) {
		t.Fatalf("changed instance: %v", err)
	}
	if err := service.Signal(t.Context(), "other-task", target, "TERM"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("another session signaled this session's process: %v", err)
	}
	if err := service.Signal(t.Context(), "task", target, "TERM"); err != nil {
		t.Fatalf("signal owned child: %v", err)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("owned child did not exit")
	}
	if _, err := service.Resolve("task", target.Reference); err == nil {
		t.Fatal("exited reference still resolves")
	}
}

func TestProcessListingIncludesHost(t *testing.T) {
	service, err := New()
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	snapshot, err := service.List(t.Context(), "task", os.Getpid(), 0, 1)
	if err != nil || len(snapshot.Processes) != 1 || snapshot.Processes[0].PID != os.Getpid() || snapshot.Processes[0].Instance == "" {
		t.Fatalf("inspect host: rows=%v err=%v", snapshot.Processes, err)
	}
	if host := snapshot.Processes[0]; host.Owned || host.Reference != "" {
		t.Fatalf("the engine's own process was offered to a session: %+v", host)
	}
}

// A process no launch of the session started is never the session's to
// signal: even a validly issued reference and an approved review are refused
// at resolution and again at delivery, and the process keeps running.
func TestProcessSignalRefusesAProcessTheSessionDidNotStart(t *testing.T) {
	service, err := New()
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	other, err := lineage.Open("command:call_2", "other-session")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	for name, line := range map[string]*lineage.Lineage{"host": nil, "other session": other} {
		t.Run(name, func(t *testing.T) {
			child, done := startSleep(t, line)
			snapshot, err := service.List(t.Context(), "task", child.Process.Pid, 0, 1)
			if err != nil || len(snapshot.Processes) != 1 {
				t.Fatalf("inspect unowned child: rows=%v err=%v", snapshot.Processes, err)
			}
			listed := snapshot.Processes[0]
			if listed.Owned || listed.Reference != "" {
				t.Fatalf("unowned process carries a signal reference: %+v", listed)
			}
			issued, err := service.encode("task", listed)
			if err != nil {
				t.Fatalf("encode reference: %v", err)
			}
			if _, err := service.Resolve("task", issued); !errors.Is(err, ErrNotOwned) {
				t.Fatalf("unowned reference resolved: %v", err)
			}
			if err := service.Signal(t.Context(), "task", listed, "KILL"); !errors.Is(err, ErrNotOwned) {
				t.Fatalf("unowned process signaled: %v", err)
			}
			select {
			case <-done:
				t.Fatal("refused signal reached the process")
			case <-time.After(100 * time.Millisecond):
			}
			if err := child.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unowned process is gone: %v", err)
			}
		})
	}
}

func TestVisibleProcessesAndProcessExists(t *testing.T) {
	self := os.Getpid()
	if !ProcessExists(self) {
		t.Fatalf("expected current process %d to exist", self)
	}
	if ProcessExists(0) || ProcessExists(-1) || ProcessExists(9999999) {
		t.Fatal("expected invalid or out-of-range PID not to exist")
	}

	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start /bin/sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	childPID := child.Process.Pid
	if !ProcessExists(childPID) {
		t.Fatalf("expected child process %d to exist", childPID)
	}

	procs, err := VisibleProcesses(t.Context())
	if err != nil {
		t.Fatalf("VisibleProcesses: %v", err)
	}
	if len(procs) == 0 {
		t.Fatal("expected non-empty visible process list")
	}
	foundChild := false
	for _, p := range procs {
		if p.PID == childPID {
			foundChild = true
			if p.Command == "" || p.Name == "" || p.Instance == "" || p.StartOrder == 0 {
				t.Fatalf("expected non-empty Command and Name for child: %+v", p)
			}
			break
		}
	}
	if !foundChild {
		t.Fatalf("expected child PID %d in VisibleProcesses", childPID)
	}
}

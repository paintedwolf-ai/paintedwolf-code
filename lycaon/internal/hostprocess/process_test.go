//go:build (darwin && cgo) || linux

package hostprocess

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessReferenceAndSignal(t *testing.T) {
	service, err := New()
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	child := exec.Command("/bin/sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start owned child: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	t.Cleanup(func() { _ = child.Process.Kill(); <-done })
	snapshot, err := service.List(t.Context(), "task", child.Process.Pid, 0, 10)
	if err != nil || snapshot.Unavailable != 0 || len(snapshot.Processes) != 1 {
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
	if err := service.Signal(t.Context(), forged, "TERM"); !errors.Is(err, ErrStale) {
		t.Fatalf("changed instance: %v", err)
	}
	if err := service.Signal(t.Context(), target, "TERM"); err != nil {
		t.Fatalf("signal owned child: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
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
}

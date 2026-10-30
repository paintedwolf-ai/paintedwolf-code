//go:build linux

package launchguard

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// Reaping adds POLLHUP to the same retained process-death notification.
func lifecyclePIDFDReady(events int16) (bool, error) {
	if events & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
		return false, fmt.Errorf("unexpected pidfd events %#x", events)
	}
	return events&(unix.POLLIN|unix.POLLHUP) != 0, nil
}

func TestLifecycleObserverRecognizesAnActuallyReapedProcess(t *testing.T) {
	child := exec.Command("/bin/sh", "-c", "read value")
	control, err := child.StdinPipe()
	testutil.FailErr(t, "hold owned process", err)
	testutil.FailErr(t, "start owned process", child.Start())
	t.Cleanup(func() { _ = control.Close(); _ = child.Process.Kill(); _ = child.Wait() })
	pin, err := unix.PidfdOpen(child.Process.Pid, 0)
	testutil.FailErr(t, "pin exact owned process", err)
	defer unix.Close(pin)
	polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
	_, err = unix.Poll(polls, 0)
	testutil.FailErr(t, "poll live owned process", err)
	ready, err := lifecyclePIDFDReady(polls[0].Revents)
	if err != nil || ready {
		t.Fatalf("live process was reported ended: ready=%v error=%v", ready, err)
	}
	testutil.FailErr(t, "release owned process", control.Close())
	if err := child.Wait(); err == nil {
		t.Fatal("read-only fixture should report its closed input")
	}
	_, err = unix.Poll(polls, 0)
	testutil.FailErr(t, "poll retained pidfd after reaping", err)
	if polls[0].Revents&unix.POLLHUP == 0 {
		t.Fatalf("kernel did not expose reaped state: %#x", polls[0].Revents)
	}
	ready, err = lifecyclePIDFDReady(polls[0].Revents)
	if err != nil || !ready {
		t.Fatalf("observer rejected the exact reaped process: events=%#x ready=%v error=%v", polls[0].Revents, ready, err)
	}
}

func TestLifecycleObserverRejectsInvalidDescriptorEvents(t *testing.T) {
	for _, events := range []int16{unix.POLLERR, unix.POLLNVAL, unix.POLLIN | unix.POLLNVAL} {
		if ready, err := lifecyclePIDFDReady(events); err == nil || ready {
			t.Fatalf("invalid descriptor event was accepted: %#x", events)
		}
	}
}

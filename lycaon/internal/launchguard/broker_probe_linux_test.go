//go:build linux

package launchguard

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEarlyBrokerProbeIgnoresUnrelatedRoleArgument(t *testing.T) {
	// Ordinary programs may carry the role spelling as data. They remain
	// dumpable and must not be mistaken for the protected launch's agent.
	bystander := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; read value", BrokerAgentArgument)
	input, err := bystander.StdinPipe()
	testutil.FailErr(t, "hold ordinary bystander", err)
	output, err := bystander.StdoutPipe()
	testutil.FailErr(t, "observe ordinary bystander", err)
	testutil.FailErr(t, "start ordinary bystander", bystander.Start())
	t.Cleanup(func() { _ = input.Close(); _ = bystander.Wait() })
	line, err := bufio.NewReader(output).ReadString('\n')
	testutil.FailErr(t, "wait for bystander readiness", err)
	if line != "ready\n" {
		t.Fatalf("ordinary bystander did not report readiness: %q", line)
	}
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", bystander.Process.Pid))
	testutil.FailErr(t, "confirm ordinary bystander is readable", err)
	testutil.FailErr(t, "close ordinary memory probe", mem.Close())

	fixture := installLaunchFixture(t)
	launch := startLaunch(t, fixture, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, "--broker-probe")
	if launch.facts.BrokerError != "" || launch.facts.BrokerExit != 0 {
		t.Fatalf("actual owned broker did not run: %+v", launch.facts)
	}
	assertBrokerAgentSealed(t, findBrokerAgentPID(t, launch.cmd.Process.Pid))
	testutil.FailErr(t, "close owned desktop", launch.stdin.Close())
	waitLaunchEnd(t, launch)
}

// probeEarlyBrokerAgent races /proc from before bootstrap start to verify a
// same-UID caller can never open /proc/<agent>/fd, /proc/<agent>/mem, or
// ptrace-attach the owned broker agent from the first observable window.
// CLONE_PARENT makes that role a direct child of this exact bootstrap; brokered
// children and unrelated commands carrying the role spelling are not agents.
func probeEarlyBrokerAgent(bootstrap *atomic.Int32, stop <-chan struct{}, result chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		select {
		case <-stop:
			result <- nil
			return
		default:
		}
		for _, pid := range brokerAgentPIDs(int(bootstrap.Load())) {
			if err := tryAttackBrokerAgent(pid); err != nil {
				result <- err
				return
			}
		}
	}
}

func brokerAgentPIDs(bootstrap int) []int {
	if bootstrap <= 1 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		birth, err := lifecycleBirth(pid)
		if err != nil || birth.Parent != bootstrap {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !bytes.Equal(raw, []byte("pw-launch-entry\x00"+BrokerAgentArgument+"\x00")) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func tryAttackBrokerAgent(pid int) error {
	for _, target := range []string{
		fmt.Sprintf("/proc/%d/mem", pid),
		fmt.Sprintf("/proc/%d/fd/4", pid),
	} {
		if file, err := os.Open(target); err == nil {
			_ = file.Close()
			return fmt.Errorf("same-UID process opened broker agent %s", target)
		}
	}
	if err := syscall.PtraceAttach(pid); err == nil {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		_ = syscall.PtraceDetach(pid)
		return fmt.Errorf("same-UID process ptrace-attached broker agent %d", pid)
	}
	return nil
}

func findBrokerAgentPID(t *testing.T, bootstrap int) int {
	t.Helper()
	for _, pid := range brokerAgentPIDs(bootstrap) {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err == nil && strings.Contains(string(raw), "NoNewPrivs:\t0\n") {
			return pid
		}
	}
	t.Fatal("broker agent process not found in /proc")
	return 0
}

func assertBrokerAgentSealed(t *testing.T, pid int) {
	t.Helper()
	for _, target := range []string{
		fmt.Sprintf("/proc/%d/mem", pid),
		fmt.Sprintf("/proc/%d/fd/4", pid),
	} {
		file, err := os.Open(target)
		if err == nil {
			_ = file.Close()
			t.Fatalf("same-UID process opened broker agent %s", target)
		}
		if !errors.Is(err, unix.EACCES) {
			t.Fatalf("open %s: %v, want EACCES", target, err)
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := syscall.PtraceAttach(pid); err == nil {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		_ = syscall.PtraceDetach(pid)
		t.Fatalf("same-UID process ptrace-attached broker agent %d", pid)
	} else if !errors.Is(err, unix.EPERM) {
		t.Fatalf("ptrace broker agent %d: %v, want EPERM", pid, err)
	}
}

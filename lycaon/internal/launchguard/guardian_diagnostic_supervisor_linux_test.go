//go:build linux

package launchguard

import (
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuardianQueuedDiagnosticFixture(t *testing.T) {
	mode := os.Getenv("PW_QUEUED_GUARDIAN_DIAGNOSTIC_FIXTURE")
	if mode == "" {
		return
	}
	state := diagnosticGuardianFixture()
	state.roots.stage = os.Getpid()
	state.control = os.NewFile(3, "owned-guardian-control")
	status := uint32(0)
	state.reportRetirement(guardianRetirement{Cause: guardianRootExited, RoleIndex: 0, WaitStatus: &status})
	if mode == "late" {
		var ready [1]byte
		gate := os.NewFile(4, "owned-settlement-gate")
		if n, err := gate.Read(ready[:]); err != nil || n != 1 {
			os.Exit(2)
		}
	}
	if mode == "failed" {
		if sendGuardianMessage(3, guardianFailed, 1) != nil {
			os.Exit(2)
		}
		status = 1
	}
	if sendGuardianMessage(3, guardianSettled, status) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestGuardianQueuedDiagnosticAndSettlementDoNotFabricateFailureNotification(t *testing.T) {
	for _, mode := range []string{"normal", "failed"} {
		t.Run(mode, func(t *testing.T) {
			pair := guardianMetadataPair(t, true)
			peerFD, err := unix.Dup(pair[0])
			testutil.FailErr(t, "give child its own fixture descriptor", err)
			peer := os.NewFile(uintptr(peerFD), "owned-guardian-peer")
			defer peer.Close()
			controlFD, err := unix.Dup(pair[1])
			testutil.FailErr(t, "give supervisor its own fixture descriptor", err)
			control := os.NewFile(uintptr(controlFD), "owned-guardian-supervisor")
			defer control.Close()
			testutil.FailErr(t, "keep supervisor drain nonblocking", unix.SetNonblock(pair[1], true))
			command := exec.Command(os.Args[0], "-test.run=^TestGuardianQueuedDiagnosticFixture$")
			command.Env = append(os.Environ(), "PW_QUEUED_GUARDIAN_DIAGNOSTIC_FIXTURE="+mode)
			command.ExtraFiles = []*os.File{peer}
			testutil.FailErr(t, "start only owned queued-report child", command.Start())
			defer command.Process.Release()
			pin, err := unix.PidfdOpen(command.Process.Pid, 0)
			testutil.FailErr(t, "pin exact owned report child", err)
			defer unix.Close(pin)
			polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
			_, err = unix.Poll(polls, 5000)
			testutil.FailErr(t, "await actual child death before supervisor begins", err)
			if polls[0].Revents&unix.POLLIN == 0 {
				t.Fatal("owned report child did not exit before supervisor check")
			}
			lifetimeR, lifetimeW, err := os.Pipe()
			testutil.FailErr(t, "create owned lifetime witness", err)
			defer lifetimeR.Close()
			defer lifetimeW.Close()
			warning, err := os.CreateTemp(t.TempDir(), "supervisor-warning-")
			testutil.FailErr(t, "capture failure presentation separately from raw diagnostic sink", err)
			defer warning.Close()
			previous := os.Stderr
			os.Stderr = warning
			defer func() { os.Stderr = previous }()
			status := superviseGuardianLaunch(lifetimeW, control, command.Process.Pid, pin,
				Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())})
			raw, err := os.ReadFile(warning.Name())
			testutil.FailErr(t, "read exact failure presentation result", err)
			expected := 0
			if mode == "failed" {
				expected = 1
			}
			if status != expected || (len(raw) != 0) != (mode == "failed") {
				t.Fatalf("diagnostic changed lifecycle notice or failure presentation: mode=%s status=%d warning=%q", mode, status, raw)
			}
		})
	}
}

func TestGuardianQueuedSettlementAfterInitialDrainIsReadBeforeDeathWarning(t *testing.T) {
	pair := guardianMetadataPair(t, true)
	peerFD, err := unix.Dup(pair[0])
	testutil.FailErr(t, "duplicate only owned report endpoint", err)
	peer := os.NewFile(uintptr(peerFD), "owned-late-report-peer")
	defer peer.Close()
	controlFD, err := unix.Dup(pair[1])
	testutil.FailErr(t, "duplicate only owned supervisor endpoint", err)
	control := os.NewFile(uintptr(controlFD), "owned-late-report-control")
	defer control.Close()
	gateR, gateW, err := os.Pipe()
	testutil.FailErr(t, "create exact child settlement gate", err)
	defer gateR.Close()
	defer gateW.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestGuardianQueuedDiagnosticFixture$")
	command.Env = append(os.Environ(), "PW_QUEUED_GUARDIAN_DIAGNOSTIC_FIXTURE=late")
	command.ExtraFiles = []*os.File{peer, gateR}
	testutil.FailErr(t, "start owned gated guardian", command.Start())
	reaped := false
	defer func() {
		if !reaped {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		} else {
			_ = command.Process.Release()
		}
	}()
	pin, err := unix.PidfdOpen(command.Process.Pid, 0)
	testutil.FailErr(t, "retain exact gated guardian pidfd", err)
	defer unix.Close(pin)
	polls := []unix.PollFd{{Fd: int32(controlFD), Events: unix.POLLIN}}
	_, err = unix.Poll(polls, 5000)
	testutil.FailErr(t, "await queued diagnostic before initial drain", err)
	if polls[0].Revents&unix.POLLIN == 0 {
		t.Fatal("owned guardian did not queue its first diagnostic")
	}
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	reports := guardianSupervisorReports{}
	reports.drain(control, command.Process.Pid, pin, caller)
	if reports.settled || reports.notified || reports.controlClosed {
		t.Fatal("initial EAGAIN was treated as settled or failed")
	}
	_, err = gateW.Write([]byte{1})
	testutil.FailErr(t, "allow only owned guardian to queue its final settlement", err)
	var status unix.WaitStatus
	actual, err := unix.Wait4(command.Process.Pid, &status, 0, nil)
	testutil.FailErr(t, "reap exact guardian after initial drain but before classification", err)
	reaped = true
	if actual != command.Process.Pid || !status.Exited() || status.ExitStatus() != 0 {
		t.Fatal("gated guardian failed before final queued settlement")
	}
	warning, err := os.CreateTemp(t.TempDir(), "late-settlement-warning-")
	testutil.FailErr(t, "capture death presentation", err)
	defer warning.Close()
	previous := os.Stderr
	os.Stderr = warning
	defer func() { os.Stderr = previous }()
	reports.guardianExited(control, command.Process.Pid, pin, caller)
	raw, err := os.ReadFile(warning.Name())
	testutil.FailErr(t, "read final death presentation", err)
	if !reports.settled || reports.status != 0 || reports.notified || len(raw) != 0 {
		t.Fatalf("final queued settlement was misclassified: %+v warning=%q", reports, raw)
	}
}

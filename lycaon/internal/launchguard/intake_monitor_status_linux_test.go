//go:build linux

package launchguard

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestIntakeMonitorStatusKeepsClosedFailurePhasesAndUnderlyingCause(t *testing.T) {
	for _, phase := range []intakeFailurePhase{intakeFailurePoll, intakeFailureControl, intakeFailureMountInfo,
		intakeFailureTopology, intakeFailureReconcile, intakeFailureHeartbeat, intakeFailureWatchdog} {
		failure := &intakeMonitorFailure{phase, unix.EIO}
		if code := MountIntakeExitStatus(failure); code != int(phase) || code <= 1 || code > 255 || !errors.Is(failure, unix.EIO) {
			t.Fatalf("lost closed nonzero phase or underlying syscall cause: phase=%d code=%d", phase, code)
		}
	}
	if MountIntakeExitStatus(nil) != 0 || MountIntakeExitStatus(unix.EIO) != 1 || MountIntakeExitStatus(
		&intakeMonitorFailure{255, unix.EIO}) != 1 {
		t.Fatal("normal, unclassified or unknown failure changed its original status")
	}
}

func TestIntakeMonitorStatusFixture(t *testing.T) {
	raw := os.Getenv("PW_INTAKE_MONITOR_STATUS_FIXTURE")
	if raw == "" {
		return
	}
	phase, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		os.Exit(2)
	}
	failure := &intakeMonitorFailure{intakeFailurePhase(phase), unix.EIO}
	if phase >= 100 && phase <= 105 {
		failure = &intakeMonitorFailure{intakeFailureTopology, &mountTopologyRefusal{predicate: topologyPredicate(phase - 100)}}
	}
	os.Exit(MountIntakeExitStatus(failure))
}

func TestIntakeMonitorStatusesRemainActualNonzeroChildExits(t *testing.T) {
	for _, code := range []int{80, 81, 82, 83, 84, 85, 86, 100, 101, 102, 103, 104, 105} {
		command := exec.Command(os.Args[0], "-test.run=^TestIntakeMonitorStatusFixture$")
		command.Env = append(os.Environ(), "PW_INTAKE_MONITOR_STATUS_FIXTURE="+strconv.Itoa(code))
		testutil.FailErr(t, "start only owned private-status child", command.Start())
		err := command.Wait()
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() != code {
			t.Fatalf("private phase%d changed failure outcome: %v", code, err)
		}
	}
}

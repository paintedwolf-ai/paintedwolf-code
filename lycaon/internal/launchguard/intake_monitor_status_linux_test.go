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
	if phase >= 110 && phase <= 115 {
		failure = &intakeMonitorFailure{intakeFailureReconcile, &mountTopologyRefusal{predicate: topologyPredicate(phase - 110)}}
	}
	if phase >= 120 && phase <= 125 {
		failure = &intakeMonitorFailure{intakeFailureReconcile, &intakeReconcileFailure{intakeReconcileLeaf(phase), unix.EIO}}
	}
	os.Exit(MountIntakeExitStatus(failure))
}

func TestIntakeMonitorStatusesRemainActualNonzeroChildExits(t *testing.T) {
	for _, code := range []int{80, 81, 82, 83, 84, 85, 86, 100, 101, 102, 103, 104, 105, 110, 111, 112, 113, 114, 115, 120, 121, 122, 123, 124, 125} {
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

func TestIntakeReconcileStatusPreservesOriginalControlCauses(t *testing.T) {
	for _, leaf := range []intakeReconcileLeaf{intakeReconcileStructural, intakeReconcileRetirement,
		intakeReconcileHiddenRoots, intakeReconcileCapture, intakeReconcileLease, intakeReconcileArrival} {
		for _, cause := range []error{unix.EIO, errIntakeEpochChanged, errIntakeDestinationUnavailable} {
			nested := &intakeReconcileFailure{leaf, cause}
			failure := &intakeMonitorFailure{intakeFailureReconcile, nested}
			if !errors.Is(failure, cause) || MountIntakeExitStatus(failure) != int(leaf) || MountIntakeExitStatus(nested) != 1 ||
				MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureHeartbeat, nested}) != 85 {
				t.Fatalf("nested diagnostic changed cause/role: leaf=%d cause=%v", leaf, cause)
			}
		}
		parser := &mountTopologyRefusal{predicate: topologyDuplicateIdentity}
		failure := &intakeMonitorFailure{intakeFailureReconcile, &intakeReconcileFailure{leaf, parser}}
		var found *mountTopologyRefusal
		if !errors.As(failure, &found) || found != parser || MountIntakeExitStatus(failure) != 112 {
			t.Fatal("nested parser lost precedence or exact underlying fact")
		}
	}
	if MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureReconcile, &intakeReconcileFailure{255, unix.EIO}}) != 84 {
		t.Fatal("unknown reconciliation leaf gained a diagnostic meaning")
	}
}

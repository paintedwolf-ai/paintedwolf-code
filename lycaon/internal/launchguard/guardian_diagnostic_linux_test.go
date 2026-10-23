//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func diagnosticGuardianFixture() protectedGuardian {
	return protectedGuardian{
		roots:            guardianRoots{stage: 41, pids: [3]int{42, 43, 44}},
		diagnosticBirths: [4]int64{101, 102, 103, 104},
	}
}

func TestGuardianRetirementDiagnosticBindsIdentityWithoutChangingLifecycle(t *testing.T) {
	state := diagnosticGuardianFixture()
	before := state
	status := uint32(unix.WaitStatus(1 << 8))
	var output bytes.Buffer
	state.writeRetirement(&output, guardianRetirement{Cause: guardianRootExited, RoleIndex: 2, WaitStatus: &status})
	if output.Len() == 0 || output.Len() > 1024 {
		t.Fatalf("retirement record length=%d outside fixed bound", output.Len())
	}
	var event guardianRetirement
	testutil.FailErr(t, "decode bounded retirement record", json.Unmarshal(output.Bytes(), &event))
	if event.Version != 1 || event.MonotonicNS <= 0 || event.Cause != guardianRootExited || event.RoleIndex != 2 || event.WaitStatus == nil || *event.WaitStatus != status {
		t.Fatalf("lost actual first-retirement facts: %+v", event)
	}
	if event.Guardian != (guardianDiagnosticProcess{"guardian", 41, 101}) || event.Roles != [3]guardianDiagnosticProcess{{"desktop", 42, 102}, {"mount-watcher", 43, 103}, {"mount-intake", 44, 104}} {
		t.Fatalf("diagnostic identities differ from captured roots: %+v", event)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("diagnostic changed admission, retirement, status or custody")
	}
	state.retired = true
	length := output.Len()
	state.writeRetirement(&output, guardianRetirement{Cause: guardianRootExited, RoleIndex: 0, WaitStatus: &status})
	if output.Len() != length {
		t.Fatal("later retirement replaced or repeated the first decision")
	}
}

type failedGuardianDiagnosticWriter struct{}

func (failedGuardianDiagnosticWriter) Write([]byte) (int, error) {
	return 0, errors.New("closed fixture diagnostic sink")
}

func TestGuardianDiagnosticFailureNeverChangesRetirementOutcome(t *testing.T) {
	for _, cause := range []guardianRetirementCause{guardianAdmissionFailed, guardianWaitFailed, guardianUnsettledChildren, guardianSignalReceived, guardianDesktopReady, guardianRoleReady, guardianHealthEnded, guardianHealthRejected, guardianHealthExpired, guardianPollFailed} {
		state := diagnosticGuardianFixture()
		before := state
		state.writeRetirement(failedGuardianDiagnosticWriter{}, guardianRetirement{Cause: cause, RoleIndex: -1})
		state.writeRetirement(nil, guardianRetirement{Cause: cause, RoleIndex: -1})
		if !reflect.DeepEqual(state, before) {
			t.Fatalf("failed %s diagnostic changed lifecycle", cause)
		}
		state.recordExit(42, unix.WaitStatus(3<<8))
		state.recordExit(43, unix.WaitStatus(0))
		state.recordExit(44, unix.WaitStatus(0))
		if !state.retired || state.failed || state.status != 3 || state.reaped != [3]bool{true, true, true} {
			t.Fatalf("diagnostic failure changed actual retirement outcome: %+v", state)
		}
	}
}

func TestGuardianDiagnosticRefusesUnknownOrUnavailableMetadata(t *testing.T) {
	state := diagnosticGuardianFixture()
	var output bytes.Buffer
	state.writeRetirement(&output, guardianRetirement{Cause: "unknown", RoleIndex: -1})
	state.writeRetirement(&output, guardianRetirement{Cause: guardianRootExited, RoleIndex: 3})
	state.diagnosticBirths[2] = 0
	state.writeRetirement(&output, guardianRetirement{Cause: guardianRootExited, RoleIndex: 2})
	if output.Len() != 0 || state.retired || state.failed || state.status != 0 {
		t.Fatal("invalid diagnostic metadata was fabricated or changed lifecycle")
	}
}

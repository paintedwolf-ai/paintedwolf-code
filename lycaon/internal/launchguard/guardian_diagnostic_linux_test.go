//go:build linux

package launchguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

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
	for _, cause := range []guardianRetirementCause{guardianWaitFailed, guardianUnsettledChildren, guardianSignalReceived, guardianDesktopReady, guardianRoleReady, guardianHealthEnded, guardianHealthRejected, guardianHealthExpired, guardianPollFailed} {
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

func TestGuardianDiagnosticSinkFixture(t *testing.T) {
	if os.Getenv("PW_GUARDIAN_DIAGNOSTIC_SINK_FIXTURE") == "" {
		return
	}
	before, err := unix.FcntlInt(2, unix.F_GETFL, 0)
	if err != nil {
		os.Exit(2)
	}
	state := diagnosticGuardianFixture()
	status := uint32(3 << 8)
	state.writeRetirement(guardianDiagnosticStderr{}, guardianRetirement{Cause: guardianRootExited, RoleIndex: 0, WaitStatus: &status})
	state.recordExit(42, unix.WaitStatus(3<<8))
	state.recordExit(43, unix.WaitStatus(0))
	state.recordExit(44, unix.WaitStatus(0))
	after, err := unix.FcntlInt(2, unix.F_GETFL, 0)
	if err != nil || before != after || before&unix.O_NONBLOCK != 0 || !state.retired || state.failed || state.status != 3 || state.reaped != [3]bool{true, true, true} {
		os.Exit(3)
	}
	_, _ = os.Stdout.Write([]byte("cleanup-completed\n"))
	os.Exit(0)
}

func TestGuardianDiagnosticBrokenOrFullBlockingStderrDoesNotPreventCleanup(t *testing.T) {
	for _, mode := range []string{"closed", "full", "regular-append"} {
		t.Run(mode, func(t *testing.T) {
			var stderr *os.File
			var regular string
			if mode == "regular-append" {
				file, err := os.CreateTemp(t.TempDir(), "diagnostic-")
				testutil.FailErr(t, "create owned diagnostic file", err)
				_, err = file.WriteString("existing diagnostic prefix\n")
				testutil.FailErr(t, "seed diagnostic file", err)
				_, err = file.Seek(0, 0)
				testutil.FailErr(t, "position inherited file before existing content", err)
				stderr, regular = file, file.Name()
			} else {
				read, write, err := os.Pipe()
				testutil.FailErr(t, "create owned stderr pipe", err)
				t.Cleanup(func() { _ = read.Close() })
				stderr = write
				if mode == "closed" {
					testutil.FailErr(t, "close only the fixture pipe reader", read.Close())
				} else {
					pipeFD := int(write.Fd())
					testutil.FailErr(t, "fill only the fixture pipe without blocking", unix.SetNonblock(pipeFD, true))
					for {
						_, err := unix.Write(pipeFD, make([]byte, 4096))
						if errors.Is(err, unix.EAGAIN) {
							break
						}
						testutil.FailErr(t, "fill bounded fixture stderr pipe", err)
					}
					testutil.FailErr(t, "restore inherited blocking stderr before launch", unix.SetNonblock(pipeFD, false))
				}
			}
			t.Cleanup(func() { _ = stderr.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGuardianDiagnosticSinkFixture$")
			cmd.Env = append(os.Environ(), "PW_GUARDIAN_DIAGNOSTIC_SINK_FIXTURE=1")
			cmd.Stderr = stderr
			out, err := cmd.Output()
			if err != nil || string(out) != "cleanup-completed\n" {
				t.Fatalf("%s stderr prevented unchanged retirement cleanup: %v %s", mode, err, out)
			}
			if regular != "" {
				raw, err := os.ReadFile(regular)
				testutil.FailErr(t, "read append-only diagnostic result", err)
				if !bytes.HasPrefix(raw, []byte("existing diagnostic prefix\n")) || bytes.Count(raw, []byte(`"version":1`)) != 1 {
					t.Fatalf("diagnostic overwrote content or repeated retirement: %s", raw)
				}
			}
		})
	}
}

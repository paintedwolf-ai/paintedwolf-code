//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func guardianDiagnosticWire(t *testing.T) []byte {
	t.Helper()
	state := diagnosticGuardianFixture()
	state.roots.stage = os.Getpid()
	var output bytes.Buffer
	status := uint32(3 << 8)
	state.writeRetirement(&output, guardianRetirement{Cause: guardianRootExited, RoleIndex: 0, WaitStatus: &status})
	return output.Bytes()
}

func TestGuardianDiagnosticTransportKeepsLifecycleAndAdmissionAuthoritySeparate(t *testing.T) {
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	pair := guardianMetadataPair(t, true)
	raw := guardianDiagnosticWire(t)
	_, err := (guardianDiagnosticControl{pair[0]}).Write(raw)
	testutil.FailErr(t, "send bounded diagnostic without descriptor authority", err)
	report, err := receiveGuardianReport(pair[1], os.Getpid(), caller)
	testutil.FailErr(t, "authenticate separate diagnostic variant", err)
	if !bytes.Equal(report.diagnostic, raw) || report.notice != (guardianNotice{}) {
		t.Fatal("diagnostic supplied a lifecycle notice or lost exact facts")
	}
	testutil.FailErr(t, "retain original settlement authority", sendGuardianMessage(pair[0], guardianSettled, 3))
	report, err = receiveGuardianReport(pair[1], os.Getpid(), caller)
	testutil.FailErr(t, "decode original settlement notice after diagnostic", err)
	if report.notice != (guardianNotice{guardianSettled, 3}) || report.diagnostic != nil {
		t.Fatal("diagnostic replaced original settlement or status")
	}
	_, err = (guardianDiagnosticControl{pair[0]}).Write(raw)
	testutil.FailErr(t, "send early diagnostic counterexample", err)
	if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil {
		t.Fatal("diagnostic substituted for admission readiness")
	}
}

func TestGuardianDiagnosticTransportRefusesForeignNoncanonicalAndAuthorityPackets(t *testing.T) {
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	raw := guardianDiagnosticWire(t)
	for name, change := range map[string]func([]byte) []byte{
		"unknown magic": func(b []byte) []byte { b[0] ^= 1; return b },
		"version":       func(b []byte) []byte { return bytes.Replace(b, []byte(`"version":1`), []byte(`"version":2`), 1) },
		"unknown field": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1)
		},
		"duplicate field": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
		},
		"cause": func(b []byte) []byte { return bytes.Replace(b, []byte(`registered_role_exited`), []byte(`settled`), 1) },
		"role":  func(b []byte) []byte { return bytes.Replace(b, []byte(`mount-intake`), []byte(`ordinary-broker`), 1) },
		"guardian pid": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"role":"guardian","pid":`), []byte(`"role":"guardian","pid":9`), 1)
		},
		"truncated": func(b []byte) []byte { return b[:len(b)-2] },
		"oversize":  func(b []byte) []byte { return append(b, make([]byte, guardianDiagnosticLimit)...) },
	} {
		t.Run(name, func(t *testing.T) {
			pair := guardianMetadataPair(t, true)
			packet := append([]byte(guardianDiagnosticMagic), raw...)
			_, err := unix.SendmsgN(pair[0], change(packet), nil, nil, unix.MSG_NOSIGNAL)
			testutil.FailErr(t, "inject independent invalid diagnostic packet", err)
			if _, err := receiveGuardianReport(pair[1], os.Getpid(), caller); err == nil {
				t.Fatal("invalid diagnostic reached lifecycle dispatch")
			}
		})
	}
	for _, credentials := range []bool{false, true} {
		pair := guardianMetadataPair(t, credentials)
		pipe := make([]int, 2)
		testutil.FailErr(t, "create injected diagnostic descriptor", unix.Pipe2(pipe, unix.O_CLOEXEC|unix.O_NONBLOCK))
		t.Cleanup(func() { _ = unix.Close(pipe[0]) })
		_, err := unix.SendmsgN(pair[0], append([]byte(guardianDiagnosticMagic), raw...), unix.UnixRights(pipe[1]), nil, unix.MSG_NOSIGNAL)
		_ = unix.Close(pipe[1])
		testutil.FailErr(t, "inject descriptor into diagnostic namespace", err)
		if _, err := receiveGuardianReport(pair[1], os.Getpid(), caller); err == nil {
			t.Fatal("diagnostic accepted rights")
		}
		var one [1]byte
		n, err := unix.Read(pipe[0], one[:])
		if n != 0 || err != nil {
			t.Fatalf("rejected diagnostic retained descriptor: %d %v", n, err)
		}
	}
	for _, expected := range []struct {
		pid    int
		caller Identity
	}{
		{os.Getpid() + 1, caller}, {os.Getpid(), Identity{UID: caller.UID ^ 1, GID: caller.GID}}, {os.Getpid(), Identity{UID: caller.UID, GID: caller.GID ^ 1}},
	} {
		pair := guardianMetadataPair(t, true)
		_, err := (guardianDiagnosticControl{pair[0]}).Write(raw)
		testutil.FailErr(t, "send real kernel identity diagnostic", err)
		if _, err := receiveGuardianReport(pair[1], expected.pid, expected.caller); err == nil {
			t.Fatal("foreign guardian diagnostic accepted")
		}
	}
	pair := guardianMetadataPair(t, false)
	_, err := (guardianDiagnosticControl{pair[0]}).Write(raw)
	testutil.FailErr(t, "send without credential transport", err)
	if _, err := receiveGuardianReport(pair[1], os.Getpid(), caller); err == nil {
		t.Fatal("diagnostic accepted absent credentials")
	}
}

func TestGuardianDiagnosticControlDropCannotBlockRetirement(t *testing.T) {
	for _, mode := range []string{"full", "closed"} {
		t.Run(mode, func(t *testing.T) {
			pair := guardianMetadataPair(t, false)
			if mode == "closed" {
				_ = unix.Shutdown(pair[1], unix.SHUT_RDWR)
			} else {
				for {
					_, err := unix.SendmsgN(pair[0], []byte("occupied"), nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
					if err == unix.EAGAIN {
						break
					}
					testutil.FailErr(t, "fill only owned control socket", err)
				}
			}
			state := diagnosticGuardianFixture()
			duplicate, err := unix.Dup(pair[0])
			testutil.FailErr(t, "pin fixture control without changing shared flags", err)
			state.control = os.NewFile(uintptr(duplicate), "owned-diagnostic-control")
			defer state.control.Close()
			state.recordExit(42, unix.WaitStatus(3<<8))
			state.recordExit(43, 0)
			state.recordExit(44, 0)
			if !state.retired || state.failed || state.status != 3 || !state.reaped[2] {
				t.Fatal("dropped diagnostic changed retirement")
			}
		})
	}
}

func TestGuardianRetirementDiagnosticCrossesRealTransition(t *testing.T) {
	fixture := installLaunchFixture(t)
	launch := startLaunch(t, fixture, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"})
	testutil.FailErr(t, "close only owned desktop input", launch.stdin.Close())
	testutil.FailErr(t, "settle actual native guardian normally", launch.wait())
	count := 0
	for _, line := range strings.Split(launch.stderr.String(), "\n") {
		var event guardianRetirement
		if json.Unmarshal([]byte(line), &event) != nil || event.Version != 1 {
			continue
		}
		count++
		if !validGuardianRetirement(event, event.Guardian.PID) || event.Guardian.PID <= 1 || event.Roles[0].PID != launch.facts.PID || uint64(event.Roles[0].Start) != launch.facts.ProcessStarts[launch.facts.PID] {
			t.Fatalf("real transition diagnostic lost exact guardian/desktop identity: %s", line)
		}
		if event.Cause != guardianRootExited && event.Cause != guardianDesktopReady {
			t.Fatalf("normal exit changed retirement cause: %s", line)
		}
		t.Logf("actual native first-retirement diagnostic: %s", line)
	}
	if count != 1 {
		t.Fatalf("actual static transition produced %d first-retirement records; stderr: %s", count, launch.stderr.String())
	}
}

// awaitStageHandoff runs the bootstrap's handoff wait against this process as
// the stage, with the peer end standing in for the stage's handoff descriptor.
func awaitStageHandoff(t *testing.T, send func(peer int)) error {
	t.Helper()
	pair := guardianMetadataPair(t, true)
	self, err := unix.PidfdOpen(os.Getpid(), 0)
	testutil.FailErr(t, "pin this process as the stage", err)
	t.Cleanup(func() { _ = unix.Close(self) })
	send(pair[0])
	owned, err := unix.Dup(pair[1])
	testutil.FailErr(t, "own the bootstrap end", err)
	control := os.NewFile(uintptr(owned), "bootstrap-guardian")
	t.Cleanup(func() { _ = control.Close() })
	return awaitGuardianHandoff(control, os.Getpid(), self, Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, nil)
}

func TestBootstrapReportsTheStageRefusalInsteadOfEOF(t *testing.T) {
	for _, reason := range []string{
		"source topology changed during structural selection",
		"initial lease attachment was not acknowledged",
		"protected stage requires the bootstrap's administrator identity",
	} {
		err := awaitStageHandoff(t, func(peer int) { sendStageRefusal(peer, errors.New(reason)) })
		var refusal *stageRefusalError
		if !errors.As(err, &refusal) || refusal.reason != reason {
			t.Fatalf("stage refusal %q reached the bootstrap as %v", reason, err)
		}
	}
	err := awaitStageHandoff(t, func(peer int) { _ = unix.Shutdown(peer, unix.SHUT_WR) })
	if !errors.Is(err, io.EOF) {
		t.Fatalf("a stage that closes without a refusal reports %v, want EOF", err)
	}
}

func TestStageRefusalIsBoundedAndPrintable(t *testing.T) {
	long := strings.Repeat("x", 2*stageRefusalLimit)
	err := awaitStageHandoff(t, func(peer int) { sendStageRefusal(peer, errors.New("bad\x1b[31m\nline "+long)) })
	var refusal *stageRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal not delivered: %v", err)
	}
	if len(refusal.reason) != stageRefusalLimit || strings.ContainsAny(refusal.reason, "\x1b\n") {
		t.Fatalf("refusal of %d bytes kept control characters or exceeded its bound", len(refusal.reason))
	}
	if _, refused := stageRefusal([]byte("PWGHAND1")); refused {
		t.Fatal("a guardian notice read as a stage refusal")
	}
}

// handoffChild starts a child the way the bootstrap starts its stage, with a
// pidfd, and returns once it has been asked to end.
func handoffChild(t *testing.T, script string) (pid, pidfd int) {
	t.Helper()
	pidfd = -1
	child := exec.Command("/bin/sh", "-c", script)
	child.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	testutil.FailErr(t, "start handoff child", child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait(); _ = unix.Close(pidfd) })
	return child.Process.Pid, pidfd
}

func TestFailedSetupNamesHowTheHandoffChildEnded(t *testing.T) {
	pid, pidfd := handoffChild(t, "exit 125")
	err := withHandoffExit(io.EOF, pidfd, pid)
	var ended *handoffExitError
	if !errors.As(err, &ended) || !errors.Is(err, io.EOF) || !ended.status.Exited() || ended.status.ExitStatus() != 125 {
		t.Fatalf("a transition refusal reached the bootstrap as %v", err)
	}
	pid, pidfd = handoffChild(t, "kill -KILL $$")
	err = withHandoffExit(io.EOF, pidfd, pid)
	if !errors.As(err, &ended) || !ended.status.Signaled() || ended.status.Signal() != syscall.SIGKILL {
		t.Fatalf("a killed setup process reached the bootstrap as %v", err)
	}
	pid, pidfd = handoffChild(t, "exec sleep 30")
	refusal := &stageRefusalError{reason: "still running"}
	if err := withHandoffExit(refusal, pidfd, pid); err != refusal {
		t.Fatalf("a live child changed the reported refusal to %v", err)
	}
	// A holder that closed the handoff but lingers is left to setup cleanup.
	if err := withHandoffExit(io.EOF, pidfd, pid); err != io.EOF {
		t.Fatalf("a lingering child changed the reported end of file to %v", err)
	}
}

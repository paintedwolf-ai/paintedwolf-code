//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGuardianRootsRejectsAmbiguousLifecycleAuthority(t *testing.T) {
	raw := make([]byte, guardianRootsBytes)
	copy(raw, "PWGROOT1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], 42)
	binary.LittleEndian.PutUint64(raw[16:], 3)
	for index, pid := range []uint64{43, 44, 45} {
		binary.LittleEndian.PutUint64(raw[24+index*8:], pid)
	}
	binary.LittleEndian.PutUint32(raw[48:], 42)
	_, err := decodeGuardianRoots(raw)
	testutil.FailErr(t, "decode exact protected lineage roots", err)
	for name, mutate := range map[string]func([]byte){
		"version":        func(b []byte) { b[8] = 2 },
		"nonleader":      func(b []byte) { b[48] = 43 },
		"zero sequence":  func(b []byte) { b[16] = 0 },
		"duplicate role": func(b []byte) { b[32] = 43 },
		"self role":      func(b []byte) { b[24] = 42 },
		"reserved":       func(b []byte) { b[63] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := append([]byte(nil), raw...)
			mutate(bad)
			if _, err := decodeGuardianRoots(bad); err == nil {
				t.Fatal("accepted ambiguous guardian authority")
			}
		})
	}
	for size := 0; size < len(raw); size++ {
		if _, err := decodeGuardianRoots(raw[:size]); err == nil {
			t.Fatalf("accepted truncated frame %d", size)
		}
	}
	if _, err := decodeGuardianRoots(append(raw, 0)); err == nil {
		t.Fatal("accepted trailing guardian data")
	}
}

// Exercises the installed setuid bootstrap, fixed static transition and real
// guardian, rather than substituting a stand-in reaper for production code.
func TestGuardianRealLaunchPreservesDesktopExitStatus(t *testing.T) {
	fixture := installLaunchFixture(t)
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"ordinary exit", []string{"--exit-status", "0"}, 0},
		{"nonzero exit", []string{"--exit-status", "3"}, 3},
		{"signal exit", []string{"--self-sigkill"}, 137},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(fixture.bootstrap, test.args...)
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("real launch failed: %v\n%s", err, output)
				}
				code = exit.ExitCode()
			}
			if code != test.code {
				t.Fatalf("real guardian exit=%d want=%d: %v\n%s", code, test.code, err, output)
			}
			if err := sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest); err != nil {
				t.Fatalf("settled real guardian retained generation lock: %v", err)
			}
		})
	}
}

func TestGuardianRegisteredExitCannotBeReplacedByReusedPID(t *testing.T) {
	state := protectedGuardian{roots: guardianRoots{pids: [3]int{101, 102, 103}}}
	state.recordExit(101, unix.WaitStatus(3<<8))
	state.recordExit(101, unix.WaitStatus(9))
	if state.status != 3 || !state.retired || !state.reaped[0] {
		t.Fatalf("reused adopted PID changed registered desktop result: %+v", state)
	}
	state.recordExit(102, unix.WaitStatus(0))
	state.recordExit(103, unix.WaitStatus(0))
	if state.failed {
		t.Fatal("expected retirement of helper roots changed normal desktop outcome")
	}
}

func TestGuardianRealLaunchWithStoreInsideLogicalInstallRoot(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "place native-style generation store beneath logical root", sudoFixture(t, "nested-generation", fixture.dir))
	command := exec.Command(fixture.bootstrap, "--exit-status", "0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native nested generation launch: %v\n%s", err, output)
	}
	testutil.FailErr(t, "nested generation launch fully settled", sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest))
}

func TestGuardianPollDescriptorPreservesABIIdentity(t *testing.T) {
	for _, fd := range []uintptr{0, 15, math.MaxInt32} {
		actual, err := guardianPollDescriptor(fd)
		testutil.FailErr(t, "encode exact poll descriptor", err)
		if uintptr(actual) != fd {
			t.Fatalf("poll descriptor changed: %d -> %d", fd, actual)
		}
	}
	for _, fd := range []uintptr{math.MaxInt32 + 1, ^uintptr(0)} {
		if actual, err := guardianPollDescriptor(fd); err == nil || actual != -1 {
			t.Fatalf("unrepresentable descriptor admitted: %d -> %d, %v", fd, actual, err)
		}
	}
}

func TestMountDescriptorIdentityRejectsNegativeFD(t *testing.T) {
	if _, _, err := mountDescriptorIdentity(-1); err == nil {
		t.Fatal("negative descriptor selected proc fdinfo")
	}
}

func TestGuardianObservationRefusesDescriptorWidthBeforePoll(t *testing.T) {
	file := os.NewFile(uintptr(math.MaxInt32)+1, "unrepresentable-health-descriptor")
	if file == nil {
		t.Fatal("construct unrepresentable descriptor observation")
	}
	defer func() { _ = file.Close() }()
	state := protectedGuardian{health: file}
	observation, err := state.observe()
	if err == nil || observation.Cause != guardianPollFailed || observation.RoleIndex != -1 {
		t.Fatalf("unrepresentable health descriptor reached poll: observation=%+v err=%v", observation, err)
	}
}

func TestGuardianImmediatePollDistinguishesInterruptFromReadiness(t *testing.T) {
	for _, final := range []error{nil, unix.EBADF} {
		calls := 0
		var ready int16 = unix.POLLIN
		fds := []unix.PollFd{{Fd: 23, Events: ready}}
		err := pollGuardianNow(fds, func(fds []unix.PollFd, timeout int) (int, error) {
			calls++
			if timeout != 0 || len(fds) != 1 || fds[0].Fd != 23 {
				t.Fatalf("unexpected pidfd observation: %v, timeout %d", fds, timeout)
			}
			if calls == 1 {
				return -1, unix.EINTR
			}
			if final != nil {
				return -1, final
			}
			fds[0].Revents = ready
			return 1, nil
		})
		if calls != 2 || !errors.Is(err, final) || (final == nil && fds[0].Revents != ready) {
			t.Fatalf("poll = %v after %d calls, readiness %d", err, calls, fds[0].Revents)
		}
	}
}

func TestGuardianRegisteredRootLiveness(t *testing.T) {
	if phase := os.Getenv("PW_TEST_GUARDIAN_ROOT_PHASE"); phase != "" {
		state := protectedGuardian{}
		alive, settled := state.registeredAlive(), state.registeredSettled()
		if alive != (phase == "live") || settled != (phase == "settled") {
			t.Fatalf("root phase %s: alive=%t settled=%t", phase, alive, settled)
		}
		if phase == "settled" {
			checkRetiredBootstrapStage(t)
		}
		return
	}
	var finish [3]func()
	files := make([]*os.File, 15)
	for i := range finish {
		cmd := exec.CommandContext(t.Context(), "/bin/cat")
		gate, err := cmd.StdinPipe()
		testutil.FailErr(t, "create root lifetime gate", err)
		var pin int
		cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pin}
		testutil.FailErr(t, "start registered root", cmd.Start())
		finish[i] = func() {
			testutil.FailErr(t, "release registered root", gate.Close())
			testutil.FailErr(t, "wait for registered root exit", cmd.Wait())
		}
		t.Cleanup(func() {
			_ = gate.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		file := os.NewFile(uintptr(pin), "registered-root")
		files[12+i] = file
		t.Cleanup(func() { _ = file.Close() })
	}
	check := func(phase string) {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGuardianRegisteredRootLiveness$")
		cmd.ExtraFiles = files
		cmd.Env = append(os.Environ(), "PW_TEST_GUARDIAN_ROOT_PHASE="+phase)
		if testing.CoverMode() != "" {
			dir := os.Getenv("PW_GO_CHILD_COVERDIR")
			if dir == "" {
				dir = os.Getenv("GOCOVERDIR")
			}
			if dir != "" {
				cmd.Args = append(cmd.Args, "-test.gocoverdir="+dir)
			}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("observe %s roots: %v\n%s", phase, err, out)
		}
	}
	check("live")
	finish[0]()
	check("mixed")
	finish[1]()
	finish[2]()
	check("settled")
}

// This fixture process owns only the setup child that the bootstrap reaps.
func checkRetiredBootstrapStage(t *testing.T) {
	t.Helper()
	stage := exec.CommandContext(t.Context(), "/bin/cat")
	gate, err := stage.StdinPipe()
	testutil.FailErr(t, "create setup gate", err)
	defer gate.Close()
	var pin int
	stage.SysProcAttr = &syscall.SysProcAttr{PidFD: &pin}
	testutil.FailErr(t, "start setup child", stage.Start())
	defer unix.Close(pin)
	defer func() { _ = stage.Process.Kill(); _ = stage.Wait() }()
	children, err := os.Open("/proc/thread-self/children")
	testutil.FailErr(t, "open bootstrap children", err)
	defer children.Close()
	proc, err := os.Open("/proc")
	testutil.FailErr(t, "open bootstrap process view", err)
	defer proc.Close()
	retireBootstrapSetup(pin, children, proc, Identity{}, nil)
	polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
	testutil.FailErr(t, "observe retired setup child", pollGuardianNow(polls, unix.Poll))
	if polls[0].Revents&unix.POLLIN == 0 {
		t.Fatal("bootstrap returned with a live setup child")
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(stage.Process.Pid, &status, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
		t.Fatalf("bootstrap retained an unreaped setup child: %v", err)
	}
}

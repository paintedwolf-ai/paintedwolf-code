//go:build linux

package exec

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

const launchMissingLandlockEnv = "PW_TEST_LAUNCH_MISSING_LANDLOCK"

func TestLaunchPlanRefusesUnavailableLinuxBoundary(t *testing.T) {
	if os.Getenv(launchMissingLandlockEnv) == "1" {
		rejectLaunchPlansWithoutLandlock(t)
		return
	}
	self, err := os.Executable()
	testutil.FailErr(t, "launch test executable", err)
	child := exec.CommandContext(t.Context(), self, "-test.run=^TestLaunchPlanRefusesUnavailableLinuxBoundary$")
	child.Env = append(os.Environ(), launchMissingLandlockEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("launch missing-capability fixture: %v: %s", err, output)
	}
}

func rejectLaunchPlansWithoutLandlock(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	testutil.FailErr(t, "fixture no-new-privileges", unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0))
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_LANDLOCK_CREATE_RULESET), Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		t.Fatalf("fixture Landlock filter: %v", errno)
	}
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	confine.TestingSetAutoConfine(t)
	for _, kind := range []LaunchKind{LaunchAgentCommand, LaunchBundledScanner, LaunchManagedBrowser, LaunchLocalMCP, LaunchDocumentCore, LaunchOCREngine} {
		err := AgentLaunch(kind, "required boundary fixture", nil).validate()
		u, typed := linuxsandbox.AsUnavailable(err)
		if !errors.Is(err, confine.ErrNotConfined) || !typed || u.Code != linuxsandbox.UnavailableLandlock {
			t.Fatalf("launch kind %s lost required boundary refusal: %v", kind, err)
		}
	}
	testutil.FailErr(t, "explicit approved host execution", AgentLaunch(LaunchAgentCommand, "host execution fixture", &confine.Confinement{HostExecution: true}).validate())
	testutil.FailErr(t, "host maintenance remains distinct", HostLaunch("host maintenance fixture").validate())
	testutil.FailErr(t, "external scanner remains distinct", ExternalScannerLaunch("user scanner fixture").validate())
}

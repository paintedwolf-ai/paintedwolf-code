//go:build linux

package browser

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

const browserMissingLandlockEnv = "PW_TEST_BROWSER_MISSING_LANDLOCK"

func TestBrowserUnavailableLinuxBoundaryDoesNotLaunch(t *testing.T) {
	if os.Getenv(browserMissingLandlockEnv) == "1" {
		refuseBrowserWithoutLandlock(t)
		return
	}
	self, err := os.Executable()
	testutil.FailErr(t, "browser test executable", err)
	child := exec.CommandContext(t.Context(), self, "-test.run=^TestBrowserUnavailableLinuxBoundaryDoesNotLaunch$")
	child.Env = append(os.Environ(), browserMissingLandlockEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("browser missing-capability fixture: %v: %s", err, output)
	}
}

func refuseBrowserWithoutLandlock(t *testing.T) {
	t.Helper()
	// This thread belongs only to the fixture child; the parent keeps its policy.
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
	box, applied, err := browserBoundary([]string{t.TempDir()})
	if box != nil || applied {
		t.Fatal("browser received a boundary with unavailable Landlock")
	}
	var rejection *browserengine.RejectError
	if !errors.As(err, &rejection) || rejection.Code != "BROWSER_UNAVAILABLE" || rejection.Data["reason"] != "confinement_unavailable" {
		t.Fatalf("browser boundary = %v, want a typed confinement refusal", err)
	}
	caps, ok := rejection.Data["linux_capabilities"].(linuxsandbox.Capabilities)
	if !ok || caps.Available() || len(caps.Unavailable) == 0 || caps.Unavailable[0].Code != linuxsandbox.UnavailableLandlock {
		t.Fatalf("browser refusal lost the unavailable Landlock fact: %+v", rejection.Data)
	}
}

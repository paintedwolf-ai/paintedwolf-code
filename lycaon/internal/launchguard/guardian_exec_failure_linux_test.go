//go:build linux

package launchguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const fixtureFaultHOME = "PW_GUARDIAN_FAULT_HOME"

func TestGuardianActualExecFailureSettlesUnadmittedLineage(t *testing.T) {
	fixture := installLaunchFixture(t)
	probe := buildGuardianCProbe(t)
	testutil.FailErr(t, "exercise fixed syscall fault through real bootstrap", sudoFixture(t, "guardian-exec-failure", fixture.dir,
		fixtureGuardianProbe+"="+probe, fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid()),
		fixtureFaultHOME+"="+t.TempDir(), fixtureHash+"="+fixture.digest))
}

func checkGuardianExecFailure(directory, probePath, uidText, gidText, home, digest string) (result error) {
	runtime.LockOSThread()
	if err := initialUserNamespace(); err != nil {
		return err
	}
	uid, e1 := strconv.ParseUint(uidText, 10, 32)
	gid, e2 := strconv.ParseUint(gidText, 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || os.Geteuid() != 0 || unix.Gettid() != os.Getpid() {
		return errors.New("fault fixture lacks initial-root leader/caller identity")
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	children, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/children", os.Getpid(), os.Getpid()))
	if err != nil {
		return err
	}
	defer children.Close()
	proc, err := os.Open("/proc")
	if err != nil {
		return err
	}
	defer proc.Close()
	defer func() { result = errors.Join(result, settleGuardianFaultFixture(children, proc, uint32(uid))) }()
	probe := filepath.Join(directory, "guardian-fault-probe")
	if _, _, err := copyRootBinary(probePath, probe, 0555, nil); err != nil {
		return err
	}
	bootstrap, err := os.Open(filepath.Join(directory, "pw-launch"))
	if err != nil {
		return err
	}
	defer bootstrap.Close()
	for _, mode := range []string{"allow", "deny"} {
		if err := runGuardianExecFault(probe, bootstrap, mode, uint32(uid), uint32(gid), home); err != nil {
			return fmt.Errorf("actual guardian exec %s: %w", mode, err)
		}
		var status unix.WaitStatus
		if pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil); pid != -1 || !errors.Is(err, unix.ECHILD) {
			return fmt.Errorf("bootstrap left an owned adopted child after %s: pid=%d status=%d err=%w", mode, pid, status, err)
		}
		if err := lockFree(filepath.Join(directory, "locks", digest+".lock")); err != nil {
			return fmt.Errorf("generation custody survived settled %s: %w", mode, err)
		}
	}
	return nil
}

func runGuardianExecFault(probe string, bootstrap *os.File, mode string, uid, gid uint32, home string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "fault-parent"), os.NewFile(uintptr(pair[1]), "fault-child")
	defer parent.Close()
	defer child.Close()
	command := exec.CommandContext(ctx, probe, "--bootstrap-fault", strconv.FormatUint(uint64(uid), 10), strconv.FormatUint(uint64(gid), 10), mode)
	command.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	command.ExtraFiles = []*os.File{bootstrap, child}
	var output, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostic
	// Creation-bound pidfd remains the signal/exit identity through both execs.
	pidfd := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	if err := command.Start(); err != nil {
		return err
	}
	defer unix.Close(pidfd)
	err = command.Wait()
	if ctx.Err() != nil {
		return errors.New("actual bootstrap did not settle within the fixture bound")
	}
	if !fixtureProcessExited(pidfd) {
		return errors.New("selected bootstrap pidfd has not exited")
	}
	armed := make([]byte, 2)
	n, _, flags, _, receive := unix.Recvmsg(int(parent.Fd()), armed, nil, unix.MSG_DONTWAIT)
	expected := byte(1)
	if mode == "deny" {
		expected = 2
	}
	if receive != nil || n != 1 || flags != 0 || armed[0] != expected {
		return errors.New("fixed seccomp prelude did not attest its selected filter")
	}
	if mode == "allow" {
		var facts protectedFacts
		if err != nil || json.Unmarshal(output.Bytes(), &facts) != nil || facts.PID <= 1 || facts.ReadError != "" || !facts.LifetimeRegular || facts.Namespace.OwnerUID != 0 || !facts.Namespace.FullUIDMap || !facts.Namespace.FullGIDMap {
			return fmt.Errorf("allow baseline did not launch protected desktop: %v stdout=%s stderr=%s", err, output.String(), diagnostic.String())
		}
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || output.Len() != 0 || diagnostic.Len() == 0 {
		return fmt.Errorf("unadmitted guardian exec failure did not refuse: %v stdout=%s stderr=%s", err, output.String(), diagnostic.String())
	}
	return nil
}

// This fixture is an isolated outer subreaper. On any failing assertion it
// retires only its actual direct/adopted children, never a PID-list snapshot.
func settleGuardianFaultFixture(children, proc *os.File, caller uint32) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) {
			return nil
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if pid > 0 {
			continue
		}
		owned, err := guardianDirectChildren(children)
		if err != nil {
			return err
		}
		for _, pid := range owned {
			if err := terminateOwnedLaunchChild(proc, pid, []uint32{0, caller}); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return errors.New("owned fault-fixture descendants remain unsettled")
		}
		time.Sleep(time.Millisecond)
	}
}

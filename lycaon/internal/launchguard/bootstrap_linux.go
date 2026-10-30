//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// StageArgument selects the privileged stage when the bootstrap executes
// itself as uid 0. A caller cannot reach the stage through the setuid
// transition: it requires uid 0 on every credential and a root-sealed frame.
const StageArgument = "--protected-stage"

// Descriptors the bootstrap hands its privileged stage.
const (
	stageFrameFD = 3 + iota
	stageGenerationFD
	stageDesktopFD
	stageEntryFD
	stageOriginFD
	stageBrokerFD
	stageLifetimeFD
	stageSelfFD
	stageServerFD
	stageMountTransitionFD
	stageGuardianFD
)

// RunBootstrap is the installed setuid program's caller-facing half. It
// captures the caller from the kernel's credential transition, selects the
// administrator's generation, starts the privileged stage, then drops every
// privilege and remains as the unprivileged supervisor of the launch.
func RunBootstrap(arguments []string) (int, error) {
	runtime.LockOSThread()
	sessionEnv := ScrubBootstrapEnvironment()
	caller, err := CaptureCaller()
	if err != nil {
		_ = DropPrivilegesForError()
		return 1, err
	}
	var streamReplacements []standardStreamReplacement
	fail := func(err error) (int, error) {
		if dropPrivileges(caller) == nil {
			reportStandardStreamReplacements(streamReplacements)
		}
		return 1, err
	}
	if unix.Gettid() != os.Getpid() {
		return fail(errors.New("bootstrap must run on the original process leader"))
	}
	streamReplacements, err = sanitizeStandardStreams()
	if err != nil {
		return fail(err)
	}
	if err := sanitizeProcess(); err != nil {
		return fail(err)
	}
	self, err := openSelf()
	if err != nil {
		return fail(err)
	}
	defer func() { _ = self.Close() }()
	generation, err := CaptureGeneration()
	if err != nil {
		return fail(err)
	}
	defer generation.Close()
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = origin.Close() }()
	broker, desktopEnd, err := brokerPair()
	if err != nil {
		return fail(err)
	}
	defer func() { _ = desktopEnd.Close() }()
	frame, err := CreateEntryFrame(generation, caller, arguments, sessionEnv)
	if err != nil {
		_ = broker.Close()
		return fail(err)
	}
	defer func() { _ = frame.Close() }()
	// Only fixed setup roles exist below this pre-admission subreaper.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		_ = broker.Close()
		return fail(err)
	}
	children, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/children", os.Getpid(), unix.Gettid()))
	if err != nil {
		_ = broker.Close()
		return fail(err)
	}
	defer func() { _ = children.Close() }()
	proc, err := os.Open("/proc")
	if err != nil {
		_ = broker.Close()
		return fail(err)
	}
	defer func() { _ = proc.Close() }()
	handoff, err := mountSocketPair("bootstrap-guardian", "guardian-bootstrap")
	if err != nil {
		_ = broker.Close()
		return fail(err)
	}
	defer func() { _ = handoff[0].Close(); _ = handoff[1].Close() }()
	if err := guardianProtocolSocket(int(handoff[0].Fd())); err != nil {
		_ = broker.Close()
		return fail(err)
	}
	stagePIDFD := -1
	stage := &exec.Cmd{
		// The child executes its own copy of the retained image, at a
		// descriptor number that survives the remapping below.
		Path:       fmt.Sprintf("/proc/self/fd/%d", stageSelfFD),
		Args:       []string{"pw-launch", StageArgument},
		Env:        []string{},
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, generation.Directory, generation.Desktop, generation.Entry, origin, desktopEnd, generation.Lifetime, self, broker, generation.MountTransition, handoff[1]},
		SysProcAttr: &syscall.SysProcAttr{
			PidFD:      &stagePIDFD,
			Cloneflags: unix.CLONE_NEWNS,
			Credential: &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}},
		},
	}
	if err := stage.Start(); err != nil {
		_ = broker.Close()
		return fail(fmt.Errorf("start protected stage: %w", err))
	}
	_ = handoff[1].Close()
	stagePin := os.NewFile(uintptr(stagePIDFD), "protected-stage-pidfd")
	defer func() { _ = stagePin.Close(); _ = stage.Process.Release() }()
	if err := awaitGuardianHandoff(handoff[0], stage.Process.Pid, stagePIDFD, caller, origin); err != nil {
		_ = unix.Shutdown(int(handoff[0].Fd()), unix.SHUT_RDWR)
		retireBootstrapSetup(stagePIDFD, children, proc, caller, generation.MountTransition)
		_ = broker.Close()
		return fail(err)
	}
	if err := dropBootstrapSupervisor(caller); err != nil {
		_ = unix.Shutdown(int(handoff[0].Fd()), unix.SHUT_RDWR)
		retireBootstrapSetup(stagePIDFD, children, proc, caller, generation.MountTransition)
		_ = broker.Close()
		return fail(err)
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0); err != nil {
		_ = unix.Shutdown(int(handoff[0].Fd()), unix.SHUT_RDWR)
		retireBootstrapSetup(stagePIDFD, children, proc, caller, generation.MountTransition)
		_ = broker.Close()
		return 1, err
	}
	if err := sendGuardianMessage(int(handoff[0].Fd()), guardianAdmit, 0); err != nil {
		_ = unix.PidfdSendSignal(stagePIDFD, unix.SIGTERM, nil, 0)
	}
	lifetime := generation.Lifetime
	generation.Lifetime = nil
	generation.Close()
	_ = frame.Close()
	_ = desktopEnd.Close()
	_ = origin.Close()
	_ = self.Close()
	_ = broker.Close()
	reportStandardStreamReplacements(streamReplacements)
	return superviseGuardianLaunch(lifetime, handoff[0], stage.Process.Pid, stagePIDFD, caller), nil
}

// sanitizeProcess removes state a caller can hand a set-ID program: the
// dumpable flag, the working directory, the umask, and descriptors beyond the
// standard three, which must themselves be open so no later descriptor takes
// their numbers.
func sanitizeProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	for fd := 0; fd < 3; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			return errors.New("bootstrap requires open standard streams")
		}
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	unix.Umask(0o022)
	return unix.Chdir("/")
}

// openSelf retains the running bootstrap image for the stage's execution,
// refusing one a non-administrator could replace.
func openSelf() (*os.File, error) {
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	if err := rootRegular(self); err != nil {
		_ = self.Close()
		return nil, err
	}
	return self, nil
}

func brokerPair() (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(pair[0]), "launch-broker"), os.NewFile(uintptr(pair[1]), "desktop-broker"), nil
}

// DropPrivilegesForError drops any elevated effective or saved identity back to
// the caller's real non-root identity before writing diagnostic output to
// inherited descriptors. It returns true only when the process is unprivileged.
func DropPrivilegesForError() bool {
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	if uid <= 0 || gid < 0 {
		return false
	}
	if egid != gid || sgid != gid {
		_ = unix.Setresgid(gid, gid, gid)
	}
	if euid != uid || suid != uid {
		_ = unix.Setresuid(uid, uid, uid)
	}
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
	ruid, reuid, rsuid := unix.Getresuid()
	rgid, regid, rsgid := unix.Getresgid()
	return ruid > 0 && reuid == ruid && rsuid == ruid && rgid >= 0 && regid == rgid && rsgid == rgid
}

// dropPrivileges leaves the supervisor as the original caller on every
// credential, non-dumpable, with no capability and no way back.
func dropPrivileges(caller Identity) error {
	if err := prctlAllThreads(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return err
	}
	groups := make([]int, len(caller.Groups))
	for i, group := range caller.Groups {
		groups[i] = int(group)
	}
	// The kernel keeps supplementary groups per thread. The standard library's
	// call reaches every thread; x/sys/unix's Setgroups is a raw call on the
	// calling thread alone, which would leave the broker agent's other threads
	// with the stage's empty list.
	if err := syscall.Setgroups(groups); err != nil {
		return err
	}
	if err := unix.Setresgid(int(caller.GID), int(caller.GID), int(caller.GID)); err != nil {
		return err
	}
	if err := unix.Setresuid(int(caller.UID), int(caller.UID), int(caller.UID)); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0); err != nil || dumpable != 0 {
		return errors.New("process remained dumpable after privilege drop")
	}
	if uid, euid, suid := unix.Getresuid(); uid != int(caller.UID) || euid != uid || suid != uid {
		return errors.New("supervisor kept a privileged identity")
	}
	return clearCapabilities()
}

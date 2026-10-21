//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
)

// RunBootstrap is the installed setuid program's caller-facing half. It
// captures the caller from the kernel's credential transition, selects the
// administrator's generation, starts the privileged stage, then drops every
// privilege and remains as the unprivileged supervisor of the launch.
func RunBootstrap(arguments []string) (int, error) {
	caller, err := CaptureCaller()
	if err != nil {
		return 1, err
	}
	if err := sanitizeProcess(); err != nil {
		return 1, err
	}
	self, err := openSelf()
	if err != nil {
		return 1, err
	}
	defer func() { _ = self.Close() }()
	generation, err := CaptureGeneration()
	if err != nil {
		return 1, err
	}
	defer generation.Close()
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return 1, err
	}
	defer func() { _ = origin.Close() }()
	broker, desktopEnd, err := brokerPair()
	if err != nil {
		return 1, err
	}
	defer func() { _ = desktopEnd.Close() }()
	frame, err := CreateEntryFrame(generation, caller, arguments, DesktopEnvironment(os.Environ(), caller.UID))
	if err != nil {
		return 1, err
	}
	defer func() { _ = frame.Close() }()
	// Descendants the stage leaves behind are reparented here, so the
	// supervisor sees the whole tree end without any cgroup authority.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 1, err
	}
	stage := &exec.Cmd{
		// The child executes its own copy of the retained image, at a
		// descriptor number that survives the remapping below.
		Path:       fmt.Sprintf("/proc/self/fd/%d", stageSelfFD),
		Args:       []string{"pw-launch", StageArgument},
		Env:        []string{},
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, generation.Directory, generation.Desktop, generation.Entry, origin, desktopEnd, generation.Lifetime, self, broker},
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags: unix.CLONE_NEWNS,
			Credential: &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}},
		},
	}
	if err := stage.Run(); err != nil {
		return 1, fmt.Errorf("privileged stage: %w", err)
	}
	lifetime := generation.Lifetime
	generation.Lifetime = nil
	generation.Close()
	_ = frame.Close()
	_ = desktopEnd.Close()
	_ = origin.Close()
	_ = self.Close()
	_ = broker.Close()
	if err := dropPrivileges(caller); err != nil {
		return 1, err
	}
	return superviseLaunch(lifetime), nil
}

// sanitizeProcess removes state a caller can hand a set-ID program: the
// working directory, the umask, and descriptors beyond the standard three,
// which must themselves be open so no later descriptor takes their numbers.
func sanitizeProcess() error {
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

// dropPrivileges leaves the supervisor as the original caller on every
// credential, with no capability and no way back.
func dropPrivileges(caller Identity) error {
	groups := make([]int, len(caller.Groups))
	for i, group := range caller.Groups {
		groups[i] = int(group)
	}
	if err := unix.Setgroups(groups); err != nil {
		return err
	}
	if err := unix.Setresgid(int(caller.GID), int(caller.GID), int(caller.GID)); err != nil {
		return err
	}
	if err := unix.Setresuid(int(caller.UID), int(caller.UID), int(caller.UID)); err != nil {
		return err
	}
	if uid, euid, suid := unix.Getresuid(); uid != int(caller.UID) || euid != uid || suid != uid {
		return errors.New("supervisor kept a privileged identity")
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if err := unix.Capget(&header, &capabilities[0]); err != nil {
		return err
	}
	if capabilities != [2]unix.CapUserData{} {
		return errors.New("supervisor kept a capability")
	}
	return nil
}

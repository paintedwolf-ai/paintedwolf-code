//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The fixed static diagnostic child inherits no generation, namespace, broker
// or lifetime authority. Its only output is the fixed message after all caller
// credentials replace root. The central bootstrap reaper still owns its exit.
func startBootstrapNotice(image *os.File, caller Identity) (_ int, _ *os.File, err error) {
	if err := initialUserNamespace(); err != nil {
		return 0, nil, err
	}
	if os.Geteuid() != 0 || caller.UID == 0 || caller.UID == ^uint32(0) || caller.GID == ^uint32(0) || len(caller.Groups) > 65536 {
		return 0, nil, errors.New("invalid setup diagnostic identity")
	}
	if err := rootRegular(image); err != nil {
		return 0, nil, err
	}
	groupCount := len(caller.Groups)
	if groupCount > 65536 {
		return 0, nil, errors.New("too many setup diagnostic groups")
	}
	raw := make([]byte, 24+4*groupCount)
	copy(raw, "PWNOTIC1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], caller.UID)
	binary.LittleEndian.PutUint32(raw[16:], caller.GID)
	binary.LittleEndian.PutUint32(raw[20:], uint32(groupCount))
	for index, group := range caller.Groups {
		if group == ^uint32(0) {
			return 0, nil, errors.New("invalid setup diagnostic group")
		}
		binary.LittleEndian.PutUint32(raw[24+4*index:], group)
	}
	fd, err := unix.MemfdCreate("painted-wolf-setup-notice", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return 0, nil, err
	}
	frame := os.NewFile(uintptr(fd), "sealed-setup-notice")
	defer func() { _ = frame.Close() }()
	if _, err := frame.Write(raw); err != nil {
		return 0, nil, err
	}
	if err := frame.Chmod(0400); err != nil {
		return 0, nil, err
	}
	if _, err := unix.FcntlInt(frame.Fd(), unix.F_ADD_SEALS, entrySeals); err != nil {
		return 0, nil, err
	}
	files := make([]*os.File, 18)
	files[0], files[17] = image, frame
	pidfd := -1
	command := &exec.Cmd{Path: "/proc/self/fd/3", Args: []string{"pw-mount-intake-transition"}, Env: []string{}, Stderr: os.Stderr, ExtraFiles: files, SysProcAttr: &syscall.SysProcAttr{PidFD: &pidfd, Credential: &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}}}}
	if err := command.Start(); err != nil {
		return 0, nil, err
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	return pid, os.NewFile(uintptr(pidfd), "setup-notice-pidfd"), nil
}

// A caller may block its stderr. Stop only this owned diagnostic child after
// one second, without changing the shared stream's status flags.
func expireBootstrapNotice(pin *os.File, deadline time.Time) {
	if pin != nil && !time.Now().Before(deadline) {
		_ = unix.PidfdSendSignal(int(pin.Fd()), unix.SIGKILL, nil, 0)
	}
}

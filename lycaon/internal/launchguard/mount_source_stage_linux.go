//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const mountSourceSetupLimit = 30 * time.Second

type mountSourceOpener struct {
	process   *exec.Cmd
	socket    *os.File
	lifetime  *os.File
	sequence  uint64
	deadline  time.Time
	finished  chan error
	timer     *time.Timer
	timerDone chan struct{}
}

// The child shares M_stage but crosses a separate root-owned user namespace.
// No mount-namespace clone occurs here: inherited mount roots must stay
// individually detachable by the short-lived initial-root stage.
func startMountSource(frame, origin, entry *os.File, topology []byte) (_ *mountSourceOpener, err error) {
	if err := initialUserNamespace(); err != nil {
		return nil, err
	}
	if _, err := parseMountTopology(topology); err != nil {
		return nil, err
	}
	sealed, err := sealSourceTopology(topology)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sealed.Close() }()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "mount-source-stage")
	child := os.NewFile(uintptr(pair[1]), "mount-source-opener")
	defer func() { _ = child.Close() }()
	read, write, err := os.Pipe()
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	defer func() { _ = read.Close() }()
	opener := &mountSourceOpener{socket: parent, lifetime: write, deadline: time.Now().Add(mountSourceSetupLimit)}
	defer func() {
		if err != nil {
			opener.close()
		}
	}()
	opener.process = &exec.Cmd{
		Path: "/proc/self/fd/" + strconv.Itoa(sourceSelfFD),
		Args: []string{"pw-launch-entry", MountSourceArgument}, Env: []string{},
		ExtraFiles: []*os.File{frame, origin, sealed, child, read, entry},
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags:                 unix.CLONE_NEWUSER,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappingsEnableSetgroups: true,
			Credential:                 &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}},
			Pdeathsig:                  unix.SIGKILL,
		},
	}
	if err := opener.process.Start(); err != nil {
		return nil, fmt.Errorf("start caller-credential mount opener: %w", err)
	}
	done := make(chan error, 1)
	opener.finished = done
	go func() { done <- opener.process.Wait() }()
	opener.armDeadline()
	return opener, nil
}

func (opener *mountSourceOpener) open(entry mountEntry) (int, error) {
	if entry.id <= 0 {
		return -1, errors.New("invalid selected mount identity")
	}
	if err := opener.boundIO(); err != nil {
		return -1, err
	}
	opener.sequence++
	request := mountTransfer{kind: mountTransferOpen, sequence: opener.sequence, id: uint64(entry.id), path: entry.mountPoint}
	if err := sendMountTransfer(int(opener.socket.Fd()), request, -1); err != nil {
		return -1, err
	}
	if err := opener.boundIO(); err != nil {
		return -1, err
	}
	message, fd, err := receiveMountTransfer(int(opener.socket.Fd()))
	if err != nil {
		return -1, err
	}
	denied := request
	denied.kind = mountTransferDenied
	if fd < 0 && message == denied {
		return -1, unix.EACCES
	}
	request.kind = mountTransferAdd
	if message != request || fd < 0 {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return -1, errors.New("mount source reply differs from its selected request")
	}
	if err := verifyMountSourceRoot(fd, entry); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func (opener *mountSourceOpener) boundIO() error {
	remaining := time.Until(opener.deadline)
	if remaining <= 0 {
		return errors.New("mount source setup deadline exceeded")
	}
	timeout := unix.NsecToTimeval(remaining.Nanoseconds())
	for _, option := range []int{unix.SO_RCVTIMEO, unix.SO_SNDTIMEO} {
		if err := unix.SetsockoptTimeval(int(opener.socket.Fd()), unix.SOL_SOCKET, option, &timeout); err != nil {
			return err
		}
	}
	return nil
}

func (opener *mountSourceOpener) finish() error {
	if err := opener.boundIO(); err != nil {
		return err
	}
	opener.sequence++
	message := mountTransfer{kind: mountTransferReady, sequence: opener.sequence}
	if err := sendMountTransfer(int(opener.socket.Fd()), message, -1); err != nil {
		return err
	}
	if err := opener.boundIO(); err != nil {
		return err
	}
	reply, fd, err := receiveMountTransfer(int(opener.socket.Fd()))
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	message.kind = mountTransferAck
	if err != nil || fd >= 0 || reply != message {
		return errors.New("mount source did not acknowledge completion")
	}
	select {
	case err := <-opener.finished:
		opener.finished = nil
		opener.stopTimer()
		return err
	case <-time.After(max(time.Until(opener.deadline), time.Nanosecond)):
		return errors.New("mount source did not exit before readiness")
	}
}

func (opener *mountSourceOpener) close() {
	opener.stopTimer()
	_ = opener.socket.Close()
	_ = opener.lifetime.Close()
	if opener.process != nil && opener.process.Process != nil {
		_ = opener.process.Process.Kill()
		// Failure cannot leave the privileged stage waiting on a stalled caller
		// filesystem. Its unprivileged child is also covered by the launch's
		// subreaper; no application readiness follows this timeout.
		if opener.finished != nil {
			select {
			case <-opener.finished:
			case <-time.After(time.Second):
			}
		}
	}
}

func (opener *mountSourceOpener) stopTimer() {
	if opener.timer == nil {
		return
	}
	if !opener.timer.Stop() {
		<-opener.timerDone
	}
	opener.timer = nil
}

func (opener *mountSourceOpener) armDeadline() {
	timerDone := make(chan struct{})
	opener.timerDone = timerDone
	opener.timer = time.AfterFunc(max(time.Until(opener.deadline), time.Nanosecond), func() {
		defer close(timerDone)
		// An absolute supervisor deadline interrupts socket I/O independently
		// of syscall EINTR retries and terminates only this owned child.
		_ = unix.Shutdown(int(opener.socket.Fd()), unix.SHUT_RDWR)
		_ = opener.process.Process.Kill()
	})
}

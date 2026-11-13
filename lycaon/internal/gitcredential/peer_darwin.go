//go:build darwin

package gitcredential

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// writeIdentified sends the helper's first bytes; the kernel records the
// connecting process, which the confinement leaves as the helper itself.
func writeIdentified(conn *net.UnixConn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

// verifyPeer admits a connection only from this executable, running as this
// user, underneath the served invocation. The kernel supplies every fact: the
// socket's peer credentials and pid, and each process's executable, parent
// and start time. The peer's start time is read before and after the checks,
// so a pid reused meanwhile refuses.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string, served servedProcess) ([]byte, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Xucred
	var peer int
	var credentialErr, pidErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		peer, pidErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return nil, err
	}
	if credentialErr != nil {
		return nil, fmt.Errorf("read peer credentials: %w", credentialErr)
	}
	if pidErr != nil {
		return nil, fmt.Errorf("read peer pid: %w", pidErr)
	}
	if int(credentials.Uid) != os.Getuid() {
		return nil, errors.New("credential helper peer runs as another user")
	}
	started, ok := osprocess.StartTime(peer)
	if !ok {
		return nil, errors.New("credential helper peer is not running")
	}
	if exe, err := processExecutable(peer); err != nil || !sameFile(exe, helper) {
		return nil, errors.New("credential helper peer is not this executable")
	}
	if err := descendsFromServedGit(peer, gitBinary, served); err != nil {
		return nil, err
	}
	if current, ok := osprocess.StartTime(peer); !ok || current != started {
		return nil, errors.New("credential helper peer exited during verification")
	}
	return nil, nil
}

func processExecutable(pid int) (string, error) { return osprocess.ExecutablePath(pid) }

func parentProcess(pid int) (int, error) {
	parent, ok := osprocess.ParentPID(pid)
	if !ok {
		return 0, fmt.Errorf("process %d is not running", pid)
	}
	return parent, nil
}

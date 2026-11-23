//go:build darwin

package githelper

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

func enableListenerCredentials(string, string, syscall.RawConn) error { return nil }

// WriteIdentified writes the helper's initial request bytes.
func WriteIdentified(conn *net.UnixConn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

// verifyPeer checks kernel peer credentials, UID, executable, Git ancestry and stable start time.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string, served ServedProcess) ([]byte, error) {
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
		return nil, errors.New("git helper peer runs as another user")
	}
	started, ok := osprocess.StartTime(peer)
	if !ok {
		return nil, errors.New("git helper peer is not running")
	}
	if exe, err := processExecutable(peer); err != nil || !sameFile(exe, helper) {
		return nil, errors.New("git helper peer is not this executable")
	}
	if err := descendsFromServedGit(peer, gitBinary, served); err != nil {
		return nil, err
	}
	if current, ok := osprocess.StartTime(peer); !ok || current != started {
		return nil, errors.New("git helper peer exited during verification")
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

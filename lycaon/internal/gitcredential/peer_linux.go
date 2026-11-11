//go:build linux

package gitcredential

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// verifyPeer admits a connection only from this executable, running as this
// user, underneath the served invocation: a git process at or below the
// started process the service is bound to. The kernel supplies every fact:
// peer credentials, each process's executable, parent and start time.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string, served servedProcess) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return fmt.Errorf("read peer credentials: %w", credentialErr)
	}
	if int(credentials.Uid) != os.Getuid() {
		return errors.New("credential helper peer runs as another user")
	}
	peer := int(credentials.Pid)
	if exe, err := processExecutable(peer); err != nil || !sameFile(exe, helper) {
		return errors.New("credential helper peer is not this executable")
	}
	return descendsFromServedGit(peer, gitBinary, served)
}

func processExecutable(pid int) (string, error) {
	return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
}

// parentProcess reads the parent from the process status record, which is
// immune to spaces or parentheses inside the command name.
func parentProcess(pid int) (int, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	record, err := osprocess.ParseLinuxStat(raw)
	if err != nil {
		return 0, err
	}
	if record.PID != pid {
		return 0, errors.New("process record names a different pid")
	}
	return record.ParentPID, nil
}

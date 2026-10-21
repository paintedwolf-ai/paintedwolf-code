//go:build linux

package gitcredential

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// maxAncestry bounds the walk from the helper to the served git process.
const maxAncestry = 16

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

// descendsFromServedGit walks up from the peer to the served process and
// requires a git process on the way, the served process included.
func descendsFromServedGit(peer int, gitBinary string, served servedProcess) error {
	sawGit := false
	pid, err := parentProcess(peer)
	for hops := 0; err == nil && pid > 1 && hops < maxAncestry; hops++ {
		if exe, exeErr := processExecutable(pid); exeErr == nil && sameFile(exe, gitBinary) {
			sawGit = true
		}
		if pid == served.pid {
			start, ok := osprocess.StartTime(pid)
			if ok && start == served.start && sawGit {
				return nil
			}
			break
		}
		pid, err = parentProcess(pid)
	}
	return errors.New("credential helper peer does not descend from the served git process")
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
	record := string(raw)
	end := strings.LastIndexByte(record, ')')
	if end < 0 {
		return 0, errors.New("invalid process record")
	}
	fields := strings.Fields(record[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("invalid process record")
	}
	return strconv.Atoi(fields[1])
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

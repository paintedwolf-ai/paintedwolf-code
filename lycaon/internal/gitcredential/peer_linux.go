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
)

// maxAncestry bounds the walk from the helper to the served git process.
const maxAncestry = 16

// verifyPeer admits a connection only from this executable, running as this
// user, underneath the git process the service was started for. The kernel
// supplies every fact: peer credentials, each process's executable and parent.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string) error {
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
	for pid, hops := peer, 0; pid > 1 && hops < maxAncestry; hops++ {
		exe, err := processExecutable(pid)
		if err == nil && sameFile(exe, gitBinary) {
			return nil
		}
		pid, err = parentProcess(pid)
		if err != nil {
			break
		}
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

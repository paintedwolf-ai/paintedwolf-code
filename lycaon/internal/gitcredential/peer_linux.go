//go:build linux

package gitcredential

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// writeIdentified sends the helper's first bytes with SCM_CREDENTIALS, which
// the kernel checks against the sending process.
func writeIdentified(conn *net.UnixConn, data []byte) error {
	pid := os.Getpid()
	if pid > math.MaxInt32 {
		return errors.New("process id exceeds the credential message")
	}
	credentials := unix.UnixCredentials(&unix.Ucred{Pid: int32(pid), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
	n, oobn, err := conn.WriteMsgUnix(data, credentials, nil)
	if err == nil && (n != len(data) || oobn != len(credentials)) {
		err = errors.New("short credential helper write")
	}
	return err
}

// verifyPeer admits a connection only from this executable, running as this
// user, underneath the served invocation. Identity comes from the helper's
// SCM_CREDENTIALS rather than SO_PEERCRED: a confinement mediator may have
// performed the connect, but only the helper itself sends. The peer's start
// time is read before and after the checks, so a pid reused meanwhile
// refuses. It returns the request bytes read with the credentials.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string, served servedProcess) ([]byte, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		optErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	}); err != nil {
		return nil, err
	}
	if optErr != nil {
		return nil, fmt.Errorf("enable peer credentials: %w", optErr)
	}
	data := make([]byte, 512)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	n, oobn, _, _, err := conn.ReadMsgUnix(data, oob)
	if err != nil {
		return nil, err
	}
	credentials, err := attestedCredentials(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if int(credentials.Uid) != os.Getuid() {
		return nil, errors.New("credential helper peer runs as another user")
	}
	peer := int(credentials.Pid)
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
	return data[:n], nil
}

func attestedCredentials(oob []byte) (*unix.Ucred, error) {
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("read peer credentials: %w", err)
	}
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_CREDENTIALS {
			credentials, err := unix.ParseUnixCredentials(&message)
			if err != nil {
				return nil, fmt.Errorf("read peer credentials: %w", err)
			}
			if credentials.Pid <= 0 {
				break
			}
			return credentials, nil
		}
	}
	return nil, errors.New("credential helper sent no credentials")
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

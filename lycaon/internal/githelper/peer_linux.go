//go:build linux

package githelper

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// enableListenerCredentials enables SO_PASSCRED before bind/listen so accepted
// sockets capture SCM_CREDENTIALS even when a helper writes before Bind completes.
func enableListenerCredentials(_ string, _ string, raw syscall.RawConn) error {
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		optErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	}); err != nil {
		return err
	}
	return optErr
}

// WriteIdentified sends the helper's initial request bytes with SCM_CREDENTIALS.
func WriteIdentified(conn *net.UnixConn, data []byte) error {
	pid := os.Getpid()
	if pid <= 0 || pid > math.MaxInt32 {
		return errors.New("process id does not fit the credential message")
	}
	uid := os.Getuid()
	if uid < 0 || uid > math.MaxUint32 {
		return errors.New("process uid does not fit the credential message")
	}
	gid := os.Getgid()
	if gid < 0 || gid > math.MaxUint32 {
		return errors.New("process gid does not fit the credential message")
	}
	credentials := unix.UnixCredentials(&unix.Ucred{Pid: int32(pid), Uid: uint32(uid), Gid: uint32(gid)})
	n, oobn, err := conn.WriteMsgUnix(data, credentials, nil)
	if err == nil && (n != len(data) || oobn != len(credentials)) {
		err = errors.New("short git helper write")
	}
	return err
}

// verifyPeer reads SCM_CREDENTIALS from the first message so identity reflects
// the sending helper even when a confinement mediator performed connect.
func verifyPeer(conn *net.UnixConn, helper, gitBinary string, served ServedProcess) ([]byte, error) {
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
	n, oobn, flags, _, err := conn.ReadMsgUnix(data, oob)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}
	if flags&unix.MSG_CTRUNC != 0 {
		return nil, errors.New("truncated peer credentials")
	}
	credentials, err := attestedCredentials(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if int(credentials.Uid) != os.Getuid() {
		return nil, errors.New("git helper peer runs as another user")
	}
	peer := int(credentials.Pid)
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
	return nil, errors.New("git helper sent no credentials")
}

func processExecutable(pid int) (string, error) {
	return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
}

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

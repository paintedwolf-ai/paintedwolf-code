//go:build linux && (amd64 || arm64)

package launchguard

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux's child siginfo union has this ABI on amd64 and arm64. x/sys exposes
// Waitid but no child-union accessor. Keep this native view explicit and checked.
type guardianChildInfo struct {
	Signo, Errno, Code int32
	_                  int32
	PID                int32
	UID                uint32
	Status             int32
	_                  [100]byte
}

var (
	_ [128 - unsafe.Sizeof(guardianChildInfo{})]byte
	_ [unsafe.Sizeof(guardianChildInfo{}) - 128]byte
	_ [unsafe.Sizeof(unix.Siginfo{}) - unsafe.Sizeof(guardianChildInfo{})]byte
	_ [unsafe.Sizeof(guardianChildInfo{}) - unsafe.Sizeof(unix.Siginfo{})]byte
	_ [16 - unsafe.Offsetof(guardianChildInfo{}.PID)]byte
	_ [unsafe.Offsetof(guardianChildInfo{}.PID) - 16]byte
	_ [20 - unsafe.Offsetof(guardianChildInfo{}.UID)]byte
	_ [unsafe.Offsetof(guardianChildInfo{}.UID) - 20]byte
	_ [unsafe.Offsetof(unix.Siginfo{}.Code) - unsafe.Offsetof(guardianChildInfo{}.Code)]byte
	_ [unsafe.Offsetof(guardianChildInfo{}.Code) - unsafe.Offsetof(unix.Siginfo{}.Code)]byte
	_ [24 - unsafe.Offsetof(guardianChildInfo{}.Status)]byte
	_ [unsafe.Offsetof(guardianChildInfo{}.Status) - 24]byte
)

func observeGuardianExit(pidfd, pid int, uid uint32) *guardianExitFact {
	var info guardianChildInfo
	// #nosec G103 -- Compile-time amd64/arm64 size and offset checks pin this exact 128-byte Waitid output.
	if unix.Waitid(unix.P_PIDFD, pidfd, (*unix.Siginfo)(unsafe.Pointer(&info)), unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil) != nil ||
		info.Signo != int32(unix.SIGCHLD) || info.Errno != 0 || int(info.PID) != pid || info.UID != uid {
		return nil
	}
	fact := guardianExitFact{Code: info.Code, Status: info.Status}
	encoded, ok := guardianExitStatus(fact.Code, fact.Status)
	if !ok {
		return nil
	}
	fact.WaitStatus = encoded
	return &fact
}

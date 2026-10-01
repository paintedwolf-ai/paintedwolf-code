//go:build darwin && cgo

package hostprocess

/*
#include <libproc.h>
#include <errno.h>
#include <signal.h>
#include <string.h>

// Public SDK headers omit the 56-byte PROC_PIDUNIQIDENTIFIERINFO layout.
struct pw_identity {
 unsigned char uuid[16];
 uint64_t unique_id, parent_unique_id;
 int32_t version, parent_version;
 uint64_t reserved[2];
};
_Static_assert(sizeof(struct pw_identity) == 56, "process identity ABI");
static int pw_identity_read(int pid, struct pw_identity *identity) {
 int n = proc_pidinfo(pid, 17, 0, identity, sizeof(*identity));
 return n == sizeof(*identity) ? 0 : (errno ? errno : ENOTSUP);
}
static int pw_signal(int pid, uint32_t version, int sig) {
 audit_token_t token = {{0}};
 token.val[5] = pid;
 token.val[7] = version;
 return proc_signal_with_audittoken(&token, sig);
}
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func listPIDs() ([]int, error) {
	n := int(C.proc_listallpids(nil, 0))
	if n <= 0 {
		return nil, ErrUnsupported
	}
	pids := make([]C.int, n+1024)
	count := int(C.proc_listallpids(unsafe.Pointer(&pids[0]), C.int(len(pids)*4)))
	if count < 0 || count >= len(pids) {
		return nil, fmt.Errorf("process snapshot changed capacity")
	}
	out := make([]int, count)
	for i := range out {
		out[i] = int(pids[i])
	}
	return out, nil
}

func inspect(pid int) (Process, error) {
	var before, after C.struct_pw_identity
	if code := C.pw_identity_read(C.int(pid), &before); code != 0 {
		return Process{}, syscall.Errno(code)
	}
	var info C.struct_proc_bsdinfo
	infoSize := C.int(C.sizeof_struct_proc_bsdinfo)
	if C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&info), infoSize) != infoSize {
		return Process{}, ErrStale
	}
	var path [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	C.proc_pidpath(C.int(pid), unsafe.Pointer(&path), C.uint32_t(len(path)))
	if code := C.pw_identity_read(C.int(pid), &after); code != 0 {
		return Process{}, syscall.Errno(code)
	}
	if before.unique_id != after.unique_id || before.version != after.version {
		return Process{}, ErrStale
	}
	return Process{PID: pid, ParentPID: int(info.pbi_ppid), UID: uint32(info.pbi_uid),
		Name: C.GoString(&info.pbi_comm[0]), Executable: C.GoString(&path[0]),
		Instance: fmt.Sprintf("%d:%d", uint64(before.unique_id), uint32(before.version))}, nil
}

func signalInstance(process Process, name string) error {
	signal, ok := map[string]C.int{"TERM": C.SIGTERM, "KILL": C.SIGKILL, "INT": C.SIGINT, "HUP": C.SIGHUP, "STOP": C.SIGSTOP, "CONT": C.SIGCONT, "USR1": C.SIGUSR1, "USR2": C.SIGUSR2}[name]
	if !ok {
		return fmt.Errorf("unsupported signal %q", name)
	}
	_, version, ok := strings.Cut(process.Instance, ":")
	if !ok {
		return ErrStale
	}
	parsed, err := strconv.ParseUint(version, 10, 32)
	if err != nil {
		return ErrStale
	}
	if code := C.pw_signal(C.int(process.PID), C.uint32_t(parsed), signal); code != 0 {
		return syscall.Errno(code)
	}
	return nil
}

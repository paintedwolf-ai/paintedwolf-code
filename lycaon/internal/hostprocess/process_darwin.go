//go:build darwin && cgo

package hostprocess

/*
#include <libproc.h>
#include <errno.h>
#include <signal.h>
#include <string.h>
#include <sys/stat.h>
#include <stdlib.h>

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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
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
	stats := ProcessStats{
		Started: time.Unix(int64(info.pbi_start_tvsec), int64(info.pbi_start_tvusec)*1000),
		State:   darwinProcessState(uint32(info.pbi_status)), TTY: "?",
	}
	if info.e_tdev != 0 && info.e_tdev != ^C.uint32_t(0) {
		if name := C.devname(C.dev_t(info.e_tdev), C.mode_t(C.S_IFCHR)); name != nil {
			stats.TTY = C.GoString(name)
		}
	}
	var task C.struct_proc_taskinfo
	if C.proc_pidinfo(C.int(pid), C.PROC_PIDTASKINFO, 0, unsafe.Pointer(&task), C.int(C.sizeof_struct_proc_taskinfo)) == C.int(C.sizeof_struct_proc_taskinfo) {
		stats.Available = true
		stats.VirtualBytes = uint64(task.pti_virtual_size)
		stats.ResidentBytes = uint64(task.pti_resident_size)
		stats.CPUTime = time.Duration(uint64(task.pti_total_user) + uint64(task.pti_total_system))
	}
	return Process{PID: pid, ParentPID: int(info.pbi_ppid), UID: uint32(info.pbi_uid),
		Name: C.GoString(&info.pbi_comm[0]), Executable: C.GoString(&path[0]),
		StartOrder: uint64(info.pbi_start_tvsec)*1_000_000 + uint64(info.pbi_start_tvusec),
		Instance:   fmt.Sprintf("%d:%d", uint64(before.unique_id), uint32(before.version)), Stats: stats}, nil
}

func darwinProcessState(code uint32) string {
	switch code {
	case 2:
		return "R"
	case 3:
		return "S"
	case 4:
		return "T"
	case 5:
		return "Z"
	default:
		return "I"
	}
}

func PhysicalMemoryBytes() uint64 {
	n, _ := unix.SysctlUint64("hw.memsize")
	return n
}

func nativeSignal(kind signalKind) (C.int, bool) {
	signal, ok := map[signalKind]C.int{
		signalTerminate: C.SIGTERM, signalKill: C.SIGKILL, signalInterrupt: C.SIGINT,
		signalHangup: C.SIGHUP, signalStop: C.SIGSTOP, signalContinue: C.SIGCONT,
		signalUser1: C.SIGUSR1, signalUser2: C.SIGUSR2,
	}[kind]
	return signal, ok
}

func signalInstance(process Process, kind signalKind) error {
	signal, ok := nativeSignal(kind)
	if !ok {
		return ErrUnsupported
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

func readCommandLine(pid int) string {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return ""
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc <= 0 || argc > 4096 {
		return ""
	}
	rest := raw[4:]
	nul := bytes.IndexByte(rest, 0)
	if nul < 0 {
		return ""
	}
	rest = rest[nul:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, min(argc, 64))
	for len(rest) > 0 && len(args) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return strings.Join(args, " ")
}

func processExistsFallback(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

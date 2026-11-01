//go:build darwin && cgo

package osprocess

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/stat.h>
#include <sys/sysctl.h>
#include <errno.h>

static int pw_directory_matches(int pid, int fd, int *matched) {
 struct proc_vnodepathinfo paths;
 struct stat expected;
 if (fstat(fd, &expected)) return errno;
 if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &paths, sizeof(paths)) != sizeof(paths)) return errno ? errno : EIO;
 struct vinfo_stat *cwd = &paths.pvi_cdir.vip_vi.vi_stat;
 *matched = cwd->vst_dev == (uint32_t)expected.st_dev && cwd->vst_ino == expected.st_ino;
 return 0;
}
static int pw_process_credentials(int pid, unsigned int result[6]) {
 struct proc_bsdinfo process;
 if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &process, sizeof(process)) != sizeof(process)) return errno ? errno : EIO;
 result[0] = process.pbi_ruid; result[1] = process.pbi_uid; result[2] = process.pbi_svuid;
 result[3] = process.pbi_rgid; result[4] = process.pbi_gid; result[5] = process.pbi_svgid;
 return 0;
}
static int pw_process_arguments(int pid, void *data, size_t *length) {
 int mib[3] = {CTL_KERN, KERN_PROCARGS2, pid};
 return sysctl(mib, 3, data, length, NULL, 0) ? errno : 0;
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func matchProcessDirectory(pid int, expected *os.File) (bool, error) {
	if expected == nil {
		return false, ErrExecutableObservationUnavailable
	}
	connection, err := expected.SyscallConn()
	if err != nil {
		return false, err
	}
	var code, matched C.int
	if err := connection.Control(func(fd uintptr) { code = C.pw_directory_matches(C.int(pid), C.int(fd), &matched) }); err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, syscall.Errno(code))
	}
	return matched != 0, nil
}
func processSecurity(pid int) (ProcessSecurity, error) {
	var data [6]C.uint
	if code := C.pw_process_credentials(C.int(pid), &data[0]); code != 0 {
		return ProcessSecurity{}, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, syscall.Errno(code))
	}
	return ProcessSecurity{RealUID: uint32(data[0]), EffectiveUID: uint32(data[1]), SavedUID: uint32(data[2]), RealGID: uint32(data[3]), EffectiveGID: uint32(data[4]), SavedGID: uint32(data[5])}, nil
}
func processArguments(pid int, maximumBytes int) ([]string, []string, bool, error) {
	var length C.size_t
	if code := C.pw_process_arguments(C.int(pid), nil, &length); code != 0 {
		return nil, nil, false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, syscall.Errno(code))
	}
	if length < 4 || uint64(length) > uint64(maximumBytes) {
		return nil, nil, false, ErrExecutableObservationUnavailable
	}
	data := make([]byte, int(length))
	if code := C.pw_process_arguments(C.int(pid), unsafe.Pointer(&data[0]), &length); code != 0 {
		return nil, nil, false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, syscall.Errno(code))
	}
	data = data[:int(length)]
	if len(data) < 4 {
		return nil, nil, false, ErrExecutableObservationUnavailable
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	if argc < 1 || argc > len(data)-4 {
		return nil, nil, false, ErrExecutableObservationUnavailable
	}
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return nil, nil, false, ErrExecutableObservationUnavailable
	}
	data = data[end+1:]
	for len(data) > 0 && data[0] == 0 {
		data = data[1:]
	}
	var args, env []string
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return nil, nil, false, ErrExecutableObservationUnavailable
		}
		args = append(args, string(data[:end]))
		data = data[end+1:]
	}
	for len(data) > 0 && data[0] != 0 {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return nil, nil, false, ErrExecutableObservationUnavailable
		}
		env = append(env, string(data[:end]))
		data = data[end+1:]
	}
	return args, env, len(env) > 0, nil
}

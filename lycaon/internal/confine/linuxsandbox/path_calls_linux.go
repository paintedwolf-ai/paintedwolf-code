//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// handoffPerformer carries the performer's socket from the inner helper.
const handoffPerformer = "performer"

// pathCallTimeout bounds one performed call; a slow filesystem can take long.
const pathCallTimeout = 60 * time.Second

// performerClient is the supervisor's side of the performer's socket. Calls
// run one at a time, so each answer belongs to the request before it.
type performerClient struct {
	mu sync.Mutex
	fd int
	// broken is set once an answer went missing.
	broken bool
}

func (c *performerClient) call(call pathCall, dirs []int) (pathResult, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return pathResult{}, -1, errors.New("the sandbox performer stopped answering")
	}
	raw, err := json.Marshal(call)
	if err != nil {
		return pathResult{}, -1, err
	}
	var rights []byte
	if len(dirs) > 0 {
		rights = unix.UnixRights(dirs...)
	}
	if err := unix.Sendmsg(c.fd, raw, rights, nil, 0); err != nil {
		c.broken = true
		return pathResult{}, -1, err
	}
	tv := unix.NsecToTimeval(pathCallTimeout.Nanoseconds())
	_ = unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	buf := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := unix.Recvmsg(c.fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil || n == 0 {
		c.broken = true
		return pathResult{}, -1, errors.New("no answer from the sandbox performer")
	}
	opened := -1
	if fds := receivedFDs(oob[:oobn]); len(fds) == 1 {
		opened = fds[0]
	} else {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
	var result pathResult
	if err := json.Unmarshal(buf[:n], &result); err != nil {
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		c.broken = true
		return pathResult{}, -1, err
	}
	return result, opened, nil
}

// isPathCall reports whether nr is a notified name-creating call.
func isPathCall(nr int32) bool {
	n := uint32(nr)
	if n == unix.SYS_OPENAT || (legacyOpen != 0 && n == legacyOpen) {
		return true
	}
	for _, list := range [][]uint32{pathCreators, legacyPathCreators} {
		for _, c := range list {
			if c == n {
				return true
			}
		}
	}
	return false
}

// pathArgs is where a call keeps each of its arguments.
type pathArgs struct {
	op string
	// dir and path index the directory and path of the name the call names
	// first; newDir and newPath the second, for link and rename. A dir of -1
	// means the working directory.
	dir, path, newDir, newPath int
	content                    int
	flags, mode, dev           int
	// fixedFlags are the open flags creat implies.
	fixedFlags uint64
	how        bool
}

const noArg = -1

func pathArgsOf(nr uint32) (pathArgs, bool) {
	a := pathArgs{dir: noArg, path: noArg, newDir: noArg, newPath: noArg, content: noArg, flags: noArg, mode: noArg, dev: noArg}
	switch nr {
	case unix.SYS_OPENAT:
		a.op, a.dir, a.path, a.flags, a.mode = callOpen, 0, 1, 2, 3
	case unix.SYS_OPENAT2:
		a.op, a.dir, a.path, a.how = callOpen, 0, 1, true
	case unix.SYS_MKDIRAT:
		a.op, a.dir, a.path, a.mode = callMkdir, 0, 1, 2
	case unix.SYS_MKNODAT:
		a.op, a.dir, a.path, a.mode, a.dev = callMknod, 0, 1, 2, 3
	case unix.SYS_SYMLINKAT:
		a.op, a.content, a.dir, a.path = callSymlink, 0, 1, 2
	case unix.SYS_LINKAT:
		a.op, a.dir, a.path, a.newDir, a.newPath, a.flags = callLink, 0, 1, 2, 3, 4
	case unix.SYS_RENAMEAT:
		a.op, a.dir, a.path, a.newDir, a.newPath = callRename, 0, 1, 2, 3
	case unix.SYS_RENAMEAT2:
		a.op, a.dir, a.path, a.newDir, a.newPath, a.flags = callRename, 0, 1, 2, 3, 4
	default:
		return legacyPathArgs(nr, a)
	}
	return a, true
}

// performPath decides and performs one notified name-creating call.
func (s *supervisor) performPath(target *targetProcess, n seccompNotif) (int64, unix.Errno, bool) {
	args, ok := pathArgsOf(uint32(n.Nr))
	performer := s.performer
	if !ok || performer == nil {
		return 0, unix.EACCES, false
	}
	call, dirs, errno := s.readPathCall(target, n, args)
	defer func() {
		for _, fd := range dirs {
			_ = unix.Close(fd)
		}
	}()
	if errno != 0 {
		return 0, errno, true
	}
	if call.Op == callOpen && call.Flags&unix.O_CREAT == 0 && !args.how {
		return 0, unix.EINVAL, true
	}
	if !target.valid() {
		return 0, unix.EINTR, true
	}
	result, opened, err := performer.call(call, dirs)
	if err != nil {
		return 0, unix.EACCES, false
	}
	if result.Refusal != nil {
		result.Refusal.Process = target.comm()
		s.refusals.report(*result.Refusal)
	}
	if opened < 0 {
		return 0, unix.Errno(result.Errno), true
	}
	defer func() { _ = unix.Close(opened) }()
	var newFlags uint32
	if call.Flags&unix.O_CLOEXEC != 0 {
		newFlags = unix.O_CLOEXEC
	}
	add := seccompNotifAddfd{ID: target.id, Flags: unix.SECCOMP_ADDFD_FLAG_SEND, SrcFD: uint32(opened), NewFDFlags: newFlags}
	fd, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(target.listener), uintptr(unix.SECCOMP_IOCTL_NOTIF_ADDFD), uintptr(unsafe.Pointer(&add)))
	if e != 0 {
		return 0, unix.EINTR, true
	}
	// The kernel already answered the call with the installed descriptor.
	return int64(fd), errAnswered, true
}

// errAnswered marks a call the kernel completed while installing its result.
const errAnswered = unix.Errno(^uintptr(0) >> 1)

// readPathCall copies a call's paths and opens the directories they resolve
// from, as they are at notification time.
func (s *supervisor) readPathCall(target *targetProcess, n seccompNotif, a pathArgs) (pathCall, []int, unix.Errno) {
	call := pathCall{Op: a.op, Flags: a.fixedFlags}
	arg := func(i int) uint64 { return n.Args[i] }
	var err error
	if call.Path, err = target.readPath(arg(a.path)); err != nil {
		return call, nil, errnoOf(err, unix.EFAULT)
	}
	if a.newPath != noArg {
		if call.NewPath, err = target.readPath(arg(a.newPath)); err != nil {
			return call, nil, errnoOf(err, unix.EFAULT)
		}
	}
	if a.content != noArg {
		if call.Content, err = target.readPath(arg(a.content)); err != nil {
			return call, nil, errnoOf(err, unix.EFAULT)
		}
	}
	if a.flags != noArg {
		call.Flags = uint64(uint32(arg(a.flags)))
	}
	if a.mode != noArg {
		call.Mode = uint32(arg(a.mode))
	}
	if a.dev != noArg {
		call.Dev = arg(a.dev)
	}
	if a.how {
		var how [24]byte
		if size := arg(3); size < uint64(len(how)) {
			return call, nil, unix.EINVAL
		}
		if err := target.read(arg(2), how[:]); err != nil {
			return call, nil, unix.EFAULT
		}
		call.Flags = binary.NativeEndian.Uint64(how[0:])
		call.Mode = uint32(binary.NativeEndian.Uint64(how[8:]))
		call.Resolve = binary.NativeEndian.Uint64(how[16:])
	}
	nsPid, nsTid := target.namespacePids(int(n.PID))
	call.Path = selfRelative(call.Path, nsPid, nsTid)
	call.NewPath = selfRelative(call.NewPath, nsPid, nsTid)
	call.Umask = target.umask()
	// Each name resolves from the directory the caller named, its working
	// directory when it named none; the kernel ignores it for an absolute path.
	dirArgs := []int{a.dir}
	if a.newPath != noArg {
		dirArgs = append(dirArgs, a.newDir)
	}
	var dirs []int
	for _, d := range dirArgs {
		dirfd := int32(unix.AT_FDCWD)
		if d != noArg {
			dirfd = int32(arg(d))
		}
		fd, err := target.openDir(dirfd)
		if err != nil {
			for _, fd := range dirs {
				_ = unix.Close(fd)
			}
			return call, nil, errnoOf(err, unix.EBADF)
		}
		dirs = append(dirs, fd)
	}
	call.Dirs = len(dirs)
	return call, dirs, 0
}

// openDir opens what a directory argument names for the caller: its working
// directory, or the object one of its descriptors holds.
func (t *targetProcess) openDir(dirfd int32) (int, error) {
	link := "/proc/" + strconv.Itoa(t.pid) + "/cwd"
	if dirfd != unix.AT_FDCWD {
		if dirfd < 0 {
			return -1, unix.EBADF
		}
		link = "/proc/" + strconv.Itoa(t.pid) + "/fd/" + strconv.Itoa(int(dirfd))
	}
	return unix.Open(link, unix.O_PATH|unix.O_CLOEXEC, 0)
}

// readPath copies one NUL-terminated path from the caller's memory, a page
// at a time so a path near the end of a mapping is not read past.
func (t *targetProcess) readPath(addr uint64) (string, error) {
	if addr == 0 {
		return "", unix.EFAULT
	}
	var out []byte
	for len(out) < unix.PathMax {
		chunk := 4096 - int(addr%4096)
		if room := unix.PathMax - len(out); chunk > room {
			chunk = room
		}
		buf := make([]byte, chunk)
		n, err := t.mem.ReadAt(buf, int64(addr))
		if n == 0 && err != nil {
			return "", unix.EFAULT
		}
		if i := indexByte(buf[:n], 0); i >= 0 {
			return string(append(out, buf[:i]...)), nil
		}
		out = append(out, buf[:n]...)
		addr += uint64(n)
	}
	return "", unix.ENAMETOOLONG
}

// umask is the caller's file creation mask; the most restrictive on failure.
func (t *targetProcess) umask() uint32 {
	value, ok := t.statusField("Umask:")
	if !ok {
		return 0o777
	}
	mask, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0o777
	}
	return uint32(mask)
}

// namespacePids are the caller's process and thread ids as the sandbox's PID
// namespace numbers them; the performer lives there.
func (t *targetProcess) namespacePids(tid int) (int, int) {
	last := func(path string) int {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "NSpid:"); ok {
				fields := strings.Fields(v)
				if len(fields) > 0 {
					id, _ := strconv.Atoi(fields[len(fields)-1])
					return id
				}
			}
		}
		return 0
	}
	pid := last("/proc/" + strconv.Itoa(t.pid) + "/status")
	return pid, last("/proc/" + strconv.Itoa(t.pid) + "/task/" + strconv.Itoa(tid) + "/status")
}

func (t *targetProcess) statusField(name string) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(t.pid) + "/status")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, name); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// selfRelative rewrites the caller's own process links, which would name the
// performer if it followed them as written.
func selfRelative(path string, pid, tid int) string {
	if pid <= 0 {
		return path
	}
	own := "/proc/" + strconv.Itoa(pid)
	for _, r := range []struct{ prefix, replacement string }{
		{"/proc/self/", own + "/"},
		{"/proc/thread-self/", own + "/task/" + strconv.Itoa(tid) + "/"},
		{"/dev/fd/", own + "/fd/"},
		{"/dev/stdin", own + "/fd/0"},
		{"/dev/stdout", own + "/fd/1"},
		{"/dev/stderr", own + "/fd/2"},
	} {
		if rest, ok := strings.CutPrefix(path, r.prefix); ok {
			return r.replacement + rest
		}
	}
	return path
}

// fdCloser closes a raw descriptor with the supervisor.
type fdCloser int

func (f fdCloser) Close() error { return unix.Close(int(f)) }

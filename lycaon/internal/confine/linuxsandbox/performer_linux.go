//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The performer creates, links, and moves names for the sandbox. The inner
// helper starts it after the Landlock ruleset applies and before the seccomp
// filter does, so it holds the sandbox's mount namespace, credentials, and
// Landlock domain without passing its own calls back to the supervisor. The
// supervisor sends it each name-creating call it was notified of, with its own
// copy of every path and the caller's directories, and it resolves the parent,
// decides the name against the floors, and performs the call on the parent it
// resolved. Nothing the caller changes afterwards reaches the kernel.

// PerformerFlag, as a binary's first argument, starts it as the sandbox's
// performer; the binary that carries the confinement helper dispatches it.
const PerformerFlag = "--create-performer"

// performerFD is the performer's end of its socket to the supervisor.
const performerFD = 3

// pathCall is one notified call, as the supervisor read it.
type pathCall struct {
	Op string `json:"op"`
	// Path is the name the call creates; for link and rename, the existing one.
	Path string `json:"path"`
	// NewPath is the name link and rename create.
	NewPath string `json:"new_path,omitempty"`
	// Content is a symlink's target, which names nothing until it is followed.
	Content string `json:"content,omitempty"`
	Flags   uint64 `json:"flags,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Dev     uint64 `json:"dev,omitempty"`
	Resolve uint64 `json:"resolve,omitempty"`
	Umask   uint32 `json:"umask"`
	// Dirs reports which directories accompany the call, in order: the one
	// Path resolves from, then the one NewPath resolves from.
	Dirs int `json:"dirs"`
}

// Calls the performer serves.
const (
	callOpen    = "open"
	callMkdir   = "mkdir"
	callMknod   = "mknod"
	callSymlink = "symlink"
	callLink    = "link"
	callRename  = "rename"
)

// pathResult is the performer's answer; an opened descriptor travels with it.
type pathResult struct {
	Errno int `json:"errno,omitempty"`
	// Refusal is set when a floor refused the call.
	Refusal *Refusal `json:"refusal,omitempty"`
}

// maxPathMessage bounds one request: two paths, a symlink target, and fields.
const maxPathMessage = 3*unix.PathMax + 1024

// startPerformer runs the performer on the restricted thread, gives it the
// floors, and hands its socket to the supervisor.
func startPerformer(fs Filesystem) error {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("performer socket: %w", err)
	}
	defer func() { _ = unix.Close(pair[0]) }()
	floors, err := json.Marshal(fs)
	if err != nil {
		_ = unix.Close(pair[1])
		return err
	}
	devnull, err := unix.Open("/dev/null", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(pair[1])
		return fmt.Errorf("performer stdio: %w", err)
	}
	_, err = syscall.ForkExec("/proc/self/exe", []string{"pw-confine-performer", PerformerFlag}, &syscall.ProcAttr{
		Env:   []string{},
		Files: []uintptr{uintptr(devnull), uintptr(devnull), 2, uintptr(pair[1])},
		Sys:   &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL},
	})
	_ = unix.Close(devnull)
	_ = unix.Close(pair[1])
	if err != nil {
		return fmt.Errorf("start performer: %w", err)
	}
	if err := unix.Sendmsg(pair[0], floors, nil, nil, 0); err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	if err := sendHandoff(handoff{Kind: handoffPerformer}, pair[0]); err != nil {
		return fmt.Errorf("hand off performer: %w", err)
	}
	return nil
}

// ServePerformer is the performer process: it sheds the capabilities the
// inner helper kept, becomes non-dumpable so no sandbox process can read or
// steer it, and answers the supervisor until it closes the socket.
func ServePerformer() error {
	runtime.LockOSThread()
	// The caller's umask applies to each call; this thread keeps its own.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("performer umask: %w", err)
	}
	if err := dropCapabilities(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("performer dumpable: %w", err)
	}
	buf := make([]byte, maxPathMessage)
	n, _, _, _, err := unix.Recvmsg(performerFD, buf, nil, 0)
	if err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	var fs Filesystem
	if err := json.Unmarshal(buf[:n], &fs); err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	floors := newCreationFloors(fs)
	oob := make([]byte, unix.CmsgSpace(4*2))
	for {
		n, oobn, _, _, err := unix.Recvmsg(performerFD, buf, oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || (n == 0 && oobn == 0) {
			return nil
		}
		dirs := receivedFDs(oob[:oobn])
		var call pathCall
		result, opened := pathResult{Errno: int(unix.EINVAL)}, -1
		if json.Unmarshal(buf[:n], &call) == nil && call.Dirs == len(dirs) {
			result, opened = floors.perform(call, dirs)
		}
		for _, fd := range dirs {
			_ = unix.Close(fd)
		}
		reply, _ := json.Marshal(result)
		var rights []byte
		if opened >= 0 {
			rights = unix.UnixRights(opened)
		}
		err = unix.Sendmsg(performerFD, reply, rights, nil, 0)
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		if err != nil {
			return nil
		}
	}
}

func receivedFDs(oob []byte) []int {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var out []int
	for i := range msgs {
		fds, err := unix.ParseUnixRights(&msgs[i])
		if err == nil {
			out = append(out, fds...)
		}
	}
	return out
}

// perform decides and runs one call; it returns an opened descriptor, or -1.
func (f creationFloors) perform(call pathCall, dirs []int) (pathResult, int) {
	dir := func(i int) int {
		if i < len(dirs) {
			return dirs[i]
		}
		return unix.AT_FDCWD
	}
	unix.Umask(int(call.Umask & 0o777))
	switch call.Op {
	case callOpen:
		return f.open(dir(0), call)
	case callMkdir, callMknod, callSymlink:
		return f.create(dir(0), call), -1
	case callLink:
		return f.link(dir(0), dir(1), call), -1
	case callRename:
		return f.rename(dir(0), dir(1), call), -1
	}
	return pathResult{Errno: int(unix.ENOSYS)}, -1
}

// parent is a name's directory, pinned, and the name's last component.
type parent struct {
	fd   int
	base string
	// path is where the name is now, in the sandbox's view.
	path string
}

func (p parent) close() {
	if p.fd >= 0 {
		_ = unix.Close(p.fd)
	}
}

// errPlainName marks a last component that names no new entry: "." or "..".
var errPlainName = errors.New("last component creates nothing")

// resolveParent pins the directory a path's last component lives in. Its
// resolution follows the caller's own rules for openat2.
func resolveParent(dirfd int, path string, resolve uint64) (parent, error) {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return parent{fd: -1}, errPlainName
	}
	dirPart, base := ".", trimmed
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		dirPart, base = trimmed[:i], trimmed[i+1:]
		if dirPart == "" {
			dirPart = "/"
		}
	}
	if base == "." || base == ".." {
		return parent{fd: -1}, errPlainName
	}
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: resolve}
	fd, err := unix.Openat2(dirfd, dirPart, &how)
	if err != nil {
		return parent{fd: -1}, err
	}
	where, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		_ = unix.Close(fd)
		return parent{fd: -1}, err
	}
	return parent{fd: fd, base: base, path: joinName(where, base)}, nil
}

func joinName(dir, base string) string {
	if dir == "/" {
		return "/" + base
	}
	return dir + "/" + base
}

// maxSymlinkHops matches the kernel's bound on followed links.
const maxSymlinkHops = 40

// followFinal resolves a final component that is a symlink, as an open that
// creates would follow it, so the floors see the name it would create.
func followFinal(p parent) (parent, error) {
	for hops := 0; hops < maxSymlinkHops; hops++ {
		var st unix.Stat_t
		if err := unix.Fstatat(p.fd, p.base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFLNK {
			return p, nil
		}
		target, err := readlinkat(p.fd, p.base)
		if err != nil {
			return p, err
		}
		from := p.fd
		if strings.HasPrefix(target, "/") {
			from = unix.AT_FDCWD
		}
		next, err := resolveParent(from, target, 0)
		p.close()
		if err != nil {
			return parent{fd: -1}, err
		}
		p = next
	}
	p.close()
	return parent{fd: -1}, unix.ELOOP
}

func readlinkat(dirfd int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dirfd, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// refusal records a floor's refusal in the macOS kernel's vocabulary.
func refusal(operation, path string) pathResult {
	return pathResult{Errno: int(unix.EACCES), Refusal: &Refusal{Operation: operation, Target: path}}
}

const (
	refusalCreate = "file-write-create"
	refusalUnlink = "file-write-unlink"
)

func errnoResult(err error) pathResult {
	if err == nil {
		return pathResult{}
	}
	return pathResult{Errno: int(errnoOf(err, unix.EIO))}
}

func (f creationFloors) open(dirfd int, call pathCall) (pathResult, int) {
	how := unix.OpenHow{Flags: call.Flags, Mode: uint64(call.Mode), Resolve: call.Resolve}
	if call.Flags&unix.O_CREAT == 0 {
		// An openat2 that creates nothing is the caller's own open.
		fd, err := unix.Openat2(dirfd, call.Path, &how)
		return errnoResult(err), fd
	}
	p, err := resolveParent(dirfd, call.Path, call.Resolve)
	if errors.Is(err, errPlainName) || (err == nil && strings.HasSuffix(call.Path, "/")) {
		p.close()
		fd, err := unix.Openat2(dirfd, call.Path, &how)
		return errnoResult(err), fd
	}
	if err != nil {
		return errnoResult(err), -1
	}
	if call.Flags&(unix.O_NOFOLLOW|unix.O_EXCL) == 0 && call.Resolve&unix.RESOLVE_NO_SYMLINKS == 0 {
		if p, err = followFinal(p); err != nil {
			return errnoResult(err), -1
		}
	}
	defer p.close()
	var st unix.Stat_t
	exists := unix.Fstatat(p.fd, p.base, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
	writes := call.Flags&unix.O_ACCMODE != unix.O_RDONLY || call.Flags&unix.O_TRUNC != 0
	if (!exists || writes) && f.refuses(createEntry, p.path) {
		return refusal(refusalCreate, p.path), -1
	}
	// The final component was resolved above; a symlink swapped in since then
	// fails rather than redirecting the create.
	final := unix.OpenHow{Flags: call.Flags | unix.O_NOFOLLOW, Mode: uint64(call.Mode), Resolve: call.Resolve & unix.RESOLVE_NO_XDEV}
	fd, err := unix.Openat2(p.fd, p.base, &final)
	return errnoResult(err), fd
}

func (f creationFloors) create(dirfd int, call pathCall) pathResult {
	p, err := resolveParent(dirfd, call.Path, 0)
	if errors.Is(err, errPlainName) {
		return errnoResult(plainCreate(dirfd, call))
	}
	if err != nil {
		return errnoResult(err)
	}
	defer p.close()
	kind := createEntry
	if call.Op == callMkdir {
		kind = createDirectory
	}
	if f.refuses(kind, p.path) {
		return refusal(refusalCreate, p.path)
	}
	switch call.Op {
	case callMkdir:
		return errnoResult(unix.Mkdirat(p.fd, p.base, call.Mode))
	case callMknod:
		return errnoResult(unix.Mknodat(p.fd, p.base, call.Mode, int(call.Dev)))
	default:
		return errnoResult(unix.Symlinkat(call.Content, p.fd, p.base))
	}
}

// plainCreate runs a create whose last component is "." or "..", which can
// only fail; the caller's own spelling chooses which error.
func plainCreate(dirfd int, call pathCall) error {
	switch call.Op {
	case callMkdir:
		return unix.Mkdirat(dirfd, call.Path, call.Mode)
	case callMknod:
		return unix.Mknodat(dirfd, call.Path, call.Mode, int(call.Dev))
	default:
		return unix.Symlinkat(call.Content, dirfd, call.Path)
	}
}

func (f creationFloors) link(olddir, newdir int, call pathCall) pathResult {
	if call.Path != "" || call.Flags&unix.AT_EMPTY_PATH == 0 {
		if old, err := resolveParent(olddir, call.Path, 0); err == nil {
			refused := f.refuses(renameSource, old.path)
			old.close()
			if refused {
				return refusal(refusalCreate, old.path)
			}
		}
	}
	p, err := resolveParent(newdir, call.NewPath, 0)
	if errors.Is(err, errPlainName) {
		return errnoResult(unix.Linkat(olddir, call.Path, newdir, call.NewPath, int(call.Flags)))
	}
	if err != nil {
		return errnoResult(err)
	}
	defer p.close()
	if f.refuses(createEntry, p.path) {
		return refusal(refusalCreate, p.path)
	}
	return errnoResult(unix.Linkat(olddir, call.Path, p.fd, p.base, int(call.Flags)))
}

func (f creationFloors) rename(olddir, newdir int, call pathCall) pathResult {
	old, err := resolveParent(olddir, call.Path, 0)
	if errors.Is(err, errPlainName) {
		return errnoResult(unix.Renameat2(olddir, call.Path, newdir, call.NewPath, uint(call.Flags)))
	}
	if err != nil {
		return errnoResult(err)
	}
	defer old.close()
	target, err := resolveParent(newdir, call.NewPath, 0)
	if errors.Is(err, errPlainName) {
		return errnoResult(unix.Renameat2(olddir, call.Path, newdir, call.NewPath, uint(call.Flags)))
	}
	if err != nil {
		return errnoResult(err)
	}
	defer target.close()
	exchange := call.Flags&unix.RENAME_EXCHANGE != 0
	switch {
	case f.refuses(renameSource, old.path), exchange && f.refuses(renameSource, target.path):
		return refusal(refusalUnlink, old.path)
	case f.refuses(renameTarget, target.path), exchange && f.refuses(renameTarget, old.path):
		return refusal(refusalCreate, target.path)
	}
	return errnoResult(unix.Renameat2(old.fd, old.base, target.fd, target.base, uint(call.Flags)))
}

//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The performer's side of each call: it pins the parent of every name the
// call would create, link, or move, decides the name against the floors, and
// performs the call on what it pinned.

// perform decides and runs one call; it returns an opened descriptor, or -1.
func (f creationFloors) perform(call pathCall, dirs []int) (pathResult, int) {
	dir := func(i int) int {
		if i < len(dirs) {
			return dirs[i]
		}
		return unix.AT_FDCWD
	}
	unix.Umask(int(call.Umask & 0o777))
	if result, opened, done := f.createPlainName(dir(0), call); done {
		return result, opened
	}
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

// createPlainName performs a create whose last component no floor can refuse
// (mayRefuseCreate) directly on the caller's directory, with this copy of its
// path. An open that would follow a final symlink is attempted without
// following; a symlink there takes the full decision, since its target is the
// name that would be created.
func (f creationFloors) createPlainName(dirfd int, call pathCall) (pathResult, int, bool) {
	base := filepath.Base(strings.TrimRight(call.Path, "/"))
	if base == "." || base == ".." || base == "/" || f.mayRefuseCreate(base) {
		return pathResult{}, -1, false
	}
	switch call.Op {
	case callOpen:
		if call.Flags&unix.O_CREAT == 0 {
			return pathResult{}, -1, false
		}
		follows := call.Flags&unix.O_NOFOLLOW == 0 && call.Resolve&unix.RESOLVE_NO_SYMLINKS == 0
		how := unix.OpenHow{Flags: call.Flags | unix.O_NOFOLLOW, Mode: uint64(call.Mode), Resolve: call.Resolve}
		fd, err := unix.Openat2(dirfd, call.Path, &how)
		if follows && errors.Is(err, unix.ELOOP) {
			return pathResult{}, -1, false
		}
		return errnoResult(err), fd, true
	case callMkdir:
		return errnoResult(unix.Mkdirat(dirfd, call.Path, call.Mode)), -1, true
	case callMknod:
		return errnoResult(unix.Mknodat(dirfd, call.Path, call.Mode, int(call.Dev))), -1, true
	case callSymlink:
		return errnoResult(unix.Symlinkat(call.Content, dirfd, call.Path)), -1, true
	}
	return pathResult{}, -1, false
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

// refusal records a floor's refusal in the macOS kernel's vocabulary. Where a
// mask already makes the location read-only the answer is the kernel's own,
// EROFS; a location no mask covers yet answers EACCES.
func refusal(operation string, p parent) pathResult {
	errno := unix.EACCES
	if readOnlyMount(p.fd, "") || readOnlyMount(p.fd, p.base) {
		errno = unix.EROFS
	}
	return pathResult{Errno: int(errno), Refusal: &Refusal{Operation: operation, Target: p.path}}
}

// readOnlyMount reports whether the entry, or the directory itself when name
// is empty, lies on a read-only mount.
func readOnlyMount(dirfd int, name string) bool {
	fd := dirfd
	if name != "" {
		opened, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return false
		}
		defer func() { _ = unix.Close(opened) }()
		fd = opened
	}
	var st unix.Statfs_t
	return unix.Fstatfs(fd, &st) == nil && st.Flags&unix.ST_RDONLY != 0
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
		return refusal(refusalCreate, p), -1
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
		return refusal(refusalCreate, p)
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
			defer old.close()
			if f.refuses(renameSource, old.path) {
				return refusal(refusalCreate, old)
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
		return refusal(refusalCreate, p)
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
		return refusal(refusalUnlink, old)
	case f.refuses(renameTarget, target.path), exchange && f.refuses(renameTarget, old.path):
		return refusal(refusalCreate, target)
	}
	return errnoResult(unix.Renameat2(old.fd, old.base, target.fd, target.base, uint(call.Flags)))
}

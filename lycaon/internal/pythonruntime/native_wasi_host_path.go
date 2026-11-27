package pythonruntime

import (
	"path"
	"strings"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)
func resolveDirRelPath(entry *nativeFD, rawRel string) (string, bool) {
	if strings.ContainsRune(rawRel, 0) {
		return "", false
	}
	joined := path.Join(entry.relPath, rawRel)
	cleaned := path.Clean("/" + strings.TrimPrefix(joined, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		cleaned = "."
	}
	return cleaned, true
}

func (h *nativeWASIHost) handlePathOpen(mem []byte, args []uint64) uint32 {
	if len(args) < 9 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[0]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	dirflags := uint32(args[1])
	pathBytes, ok := memSlice(mem, uint32(args[2]), uint32(args[3]))
	if !ok {
		return wasiErrnoFault
	}
	oflags := uint32(args[4])
	rightsBase := args[5]
	fdflags := uint32(args[7])
	outFDPtr := uint32(args[8])

	rel, valid := resolveDirRelPath(dirEntry, string(pathBytes))
	if !valid {
		return wasiErrnoInval
	}

	var sysOflag experimentalsys.Oflag
	wantRead := rightsBase&wasiRightFdRead != 0
	wantWrite := rightsBase&wasiRightFdWrite != 0 || oflags&(wasiOflagCreat|wasiOflagTrunc) != 0 || fdflags&wasiFdflagAppend != 0
	switch {
	case wantRead && wantWrite:
		sysOflag = experimentalsys.O_RDWR
	case wantWrite:
		sysOflag = experimentalsys.O_WRONLY
	default:
		sysOflag = experimentalsys.O_RDONLY
	}
	if oflags&wasiOflagCreat != 0 {
		sysOflag |= experimentalsys.O_CREAT
	}
	if oflags&wasiOflagExcl != 0 {
		sysOflag |= experimentalsys.O_EXCL
	}
	if oflags&wasiOflagTrunc != 0 {
		sysOflag |= experimentalsys.O_TRUNC
	}
	if oflags&wasiOflagDirectory != 0 {
		sysOflag |= experimentalsys.O_DIRECTORY
	}
	if fdflags&wasiFdflagAppend != 0 {
		sysOflag |= experimentalsys.O_APPEND
	}
	if dirflags&1 == 0 {
		sysOflag |= experimentalsys.O_NOFOLLOW
	}

	f, errno := dirEntry.fs.OpenFile(rel, sysOflag, 0o644)
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	isDir := oflags&wasiOflagDirectory != 0
	if !isDir {
		if st, serr := f.Stat(); serr == 0 && st.Mode.IsDir() {
			isDir = true
		}
	}

	h.mu.Lock()
	newFD := h.nextFD
	h.nextFD++
	h.fds[newFD] = &nativeFD{
		fs:      dirEntry.fs,
		relPath: rel,
		file:    f,
		isDir:   isDir,
		fdflags: uint16(fdflags),
	}
	h.mu.Unlock()

	if !putU32(mem, outFDPtr, newFD) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handlePathFilestatGet(mem []byte, args []uint64) uint32 {
	if len(args) < 5 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[0]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	flags := uint32(args[1])
	pathBytes, ok := memSlice(mem, uint32(args[2]), uint32(args[3]))
	if !ok {
		return wasiErrnoFault
	}
	rel, valid := resolveDirRelPath(dirEntry, string(pathBytes))
	if !valid {
		return wasiErrnoInval
	}
	var (
		st    sys.Stat_t
		errno experimentalsys.Errno
	)
	if flags&1 != 0 {
		st, errno = dirEntry.fs.Stat(rel)
	} else {
		st, errno = dirEntry.fs.Lstat(rel)
	}
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	if !writeFilestat(mem, uint32(args[4]), st) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handlePathFilestatSetTimes(mem []byte, args []uint64) uint32 {
	if len(args) < 7 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[0]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	pathBytes, ok := memSlice(mem, uint32(args[2]), uint32(args[3]))
	if !ok {
		return wasiErrnoFault
	}
	rel, valid := resolveDirRelPath(dirEntry, string(pathBytes))
	if !valid {
		return wasiErrnoInval
	}
	atim, mtim := resolveWasiTimes(h.clock, int64(args[4]), int64(args[5]), uint32(args[6]))
	return sysErrnoToWASI(dirEntry.fs.Utimens(rel, atim, mtim))
}

func (h *nativeWASIHost) handlePathSimple(mem []byte, args []uint64, op string) uint32 {
	if len(args) < 3 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[0]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	pathBytes, ok := memSlice(mem, uint32(args[1]), uint32(args[2]))
	if !ok {
		return wasiErrnoFault
	}
	rel, valid := resolveDirRelPath(dirEntry, string(pathBytes))
	if !valid {
		return wasiErrnoInval
	}
	switch op {
	case "mkdir":
		return sysErrnoToWASI(dirEntry.fs.Mkdir(rel, 0o755))
	case "rmdir":
		return sysErrnoToWASI(dirEntry.fs.Rmdir(rel))
	case "unlink":
		return sysErrnoToWASI(dirEntry.fs.Unlink(rel))
	default:
		return wasiErrnoNosys
	}
}

func (h *nativeWASIHost) handlePathRename(mem []byte, args []uint64) uint32 {
	if len(args) < 6 {
		return wasiErrnoInval
	}
	oldDir, ok := h.getFD(uint32(args[0]))
	if !ok || !oldDir.isDir {
		return wasiErrnoBadf
	}
	newDir, ok := h.getFD(uint32(args[3]))
	if !ok || !newDir.isDir {
		return wasiErrnoBadf
	}
	if oldDir.fs != newDir.fs {
		return wasiErrnoNotsup
	}
	oldBytes, ok := memSlice(mem, uint32(args[1]), uint32(args[2]))
	if !ok {
		return wasiErrnoFault
	}
	newBytes, ok := memSlice(mem, uint32(args[4]), uint32(args[5]))
	if !ok {
		return wasiErrnoFault
	}
	oldRel, ok1 := resolveDirRelPath(oldDir, string(oldBytes))
	newRel, ok2 := resolveDirRelPath(newDir, string(newBytes))
	if !ok1 || !ok2 {
		return wasiErrnoInval
	}
	return sysErrnoToWASI(oldDir.fs.Rename(oldRel, newRel))
}

func (h *nativeWASIHost) handlePathReadlink(mem []byte, args []uint64) uint32 {
	if len(args) < 6 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[0]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	pathBytes, ok := memSlice(mem, uint32(args[1]), uint32(args[2]))
	if !ok {
		return wasiErrnoFault
	}
	rel, valid := resolveDirRelPath(dirEntry, string(pathBytes))
	if !valid {
		return wasiErrnoInval
	}
	target, errno := dirEntry.fs.Readlink(rel)
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	dst, ok := memSlice(mem, uint32(args[3]), uint32(args[4]))
	if !ok {
		return wasiErrnoFault
	}
	n := copy(dst, target)
	if !putU32(mem, uint32(args[5]), uint32(n)) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handlePathLink(mem []byte, args []uint64) uint32 {
	if len(args) < 7 {
		return wasiErrnoInval
	}
	oldDir, ok := h.getFD(uint32(args[0]))
	if !ok || !oldDir.isDir {
		return wasiErrnoBadf
	}
	newDir, ok := h.getFD(uint32(args[4]))
	if !ok || !newDir.isDir {
		return wasiErrnoBadf
	}
	if oldDir.fs != newDir.fs {
		return wasiErrnoNotsup
	}
	oldBytes, ok := memSlice(mem, uint32(args[2]), uint32(args[3]))
	if !ok {
		return wasiErrnoFault
	}
	newBytes, ok := memSlice(mem, uint32(args[5]), uint32(args[6]))
	if !ok {
		return wasiErrnoFault
	}
	oldRel, ok1 := resolveDirRelPath(oldDir, string(oldBytes))
	newRel, ok2 := resolveDirRelPath(newDir, string(newBytes))
	if !ok1 || !ok2 {
		return wasiErrnoInval
	}
	return sysErrnoToWASI(oldDir.fs.Link(oldRel, newRel))
}

func (h *nativeWASIHost) handlePathSymlink(mem []byte, args []uint64) uint32 {
	if len(args) < 5 {
		return wasiErrnoInval
	}
	dirEntry, ok := h.getFD(uint32(args[2]))
	if !ok || !dirEntry.isDir {
		return wasiErrnoBadf
	}
	oldBytes, ok := memSlice(mem, uint32(args[0]), uint32(args[1]))
	if !ok {
		return wasiErrnoFault
	}
	newBytes, ok := memSlice(mem, uint32(args[3]), uint32(args[4]))
	if !ok {
		return wasiErrnoFault
	}
	newRel, valid := resolveDirRelPath(dirEntry, string(newBytes))
	if !valid {
		return wasiErrnoInval
	}
	return sysErrnoToWASI(dirEntry.fs.Symlink(string(oldBytes), newRel))
}

func resolveWasiTimes(clk Clock, atim, mtim int64, fstFlags uint32) (int64, int64) {
	now := clk.Now().UnixNano()
	outA := experimentalsys.UTIME_OMIT
	outM := experimentalsys.UTIME_OMIT
	if fstFlags&1 != 0 {
		outA = atim
	} else if fstFlags&2 != 0 {
		outA = now
	}
	if fstFlags&4 != 0 {
		outM = mtim
	} else if fstFlags&8 != 0 {
		outM = now
	}
	return outA, outM
}

func sysErrnoToWASI(e experimentalsys.Errno) uint32 {
	switch e {
	case 0:
		return wasiErrnoSuccess
	case experimentalsys.EACCES:
		return wasiErrnoAcces
	case experimentalsys.EAGAIN:
		return wasiErrnoAgain
	case experimentalsys.EBADF:
		return wasiErrnoBadf
	case experimentalsys.EEXIST:
		return wasiErrnoExist
	case experimentalsys.EINVAL:
		return wasiErrnoInval
	case experimentalsys.EIO:
		return wasiErrnoIO
	case experimentalsys.EISDIR:
		return wasiErrnoIsdir
	case experimentalsys.ENOENT:
		return wasiErrnoNoent
	case experimentalsys.ENOSYS:
		return wasiErrnoNosys
	case experimentalsys.ENOTDIR:
		return wasiErrnoNotdir
	case experimentalsys.ENOTEMPTY:
		return wasiErrnoNotempty
	case experimentalsys.ENOTSUP:
		return wasiErrnoNotsup
	case experimentalsys.EPERM:
		return wasiErrnoPerm
	case experimentalsys.EROFS:
		return wasiErrnoRofs
	default:
		return wasiErrnoIO
	}
}

// hostDirSysFS adapts a local host directory (read-only image stdlib/packages
// or writable per-invocation scratch) to experimentalsys.FS for nativeWASIHost.

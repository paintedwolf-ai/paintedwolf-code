package pythonruntime

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"sync/atomic"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)

// OflagToOpenFlags converts a wazero experimental/sys.Oflag bitmask into
// pythonruntime.OpenFlags.
func OflagToOpenFlags(flag experimentalsys.Oflag) OpenFlags {
	var out OpenFlags
	switch flag & 0x3 {
	case experimentalsys.O_WRONLY:
		out |= OpenWrite
	case experimentalsys.O_RDWR:
		out |= OpenRead | OpenWrite
	default:
		out |= OpenRead
	}
	if flag&experimentalsys.O_CREAT != 0 {
		out |= OpenCreate
	}
	if flag&experimentalsys.O_EXCL != 0 {
		out |= OpenExclusive
	}
	if flag&experimentalsys.O_TRUNC != 0 {
		out |= OpenTruncate
	}
	if flag&experimentalsys.O_APPEND != 0 {
		out |= OpenAppend
	}
	if flag&experimentalsys.O_DIRECTORY != 0 {
		out |= OpenDirectory
	}
	if flag&experimentalsys.O_NOFOLLOW != 0 {
		out |= OpenNoFollow
	}
	return out
}

// OpenFlagsToOflag converts pythonruntime.OpenFlags into a wazero
// experimental/sys.Oflag bitmask.
func OpenFlagsToOflag(flags OpenFlags) experimentalsys.Oflag {
	var out experimentalsys.Oflag
	hasRead := flags&OpenRead != 0
	hasWrite := flags&OpenWrite != 0
	switch {
	case hasRead && hasWrite:
		out = experimentalsys.O_RDWR
	case hasWrite:
		out = experimentalsys.O_WRONLY
	default:
		out = experimentalsys.O_RDONLY
	}
	if flags&OpenCreate != 0 {
		out |= experimentalsys.O_CREAT
	}
	if flags&OpenExclusive != 0 {
		out |= experimentalsys.O_EXCL
	}
	if flags&OpenTruncate != 0 {
		out |= experimentalsys.O_TRUNC
	}
	if flags&OpenAppend != 0 {
		out |= experimentalsys.O_APPEND
	}
	if flags&OpenDirectory != 0 {
		out |= experimentalsys.O_DIRECTORY
	}
	if flags&OpenNoFollow != 0 {
		out |= experimentalsys.O_NOFOLLOW
	}
	return out
}

// ErrnoToSys maps pythonruntime.Errno to wazero experimental/sys.Errno.
// ErrnoEXDEV maps to ENOTSUP and ErrnoENOSPC maps to EIO because wazero's
// experimental/sys.Errno subset omits EXDEV and ENOSPC.
func ErrnoToSys(e Errno) experimentalsys.Errno {
	switch e {
	case ErrnoSuccess:
		return 0
	case ErrnoENOENT:
		return experimentalsys.ENOENT
	case ErrnoEEXIST:
		return experimentalsys.EEXIST
	case ErrnoEACCES:
		return experimentalsys.EACCES
	case ErrnoEPERM:
		return experimentalsys.EPERM
	case ErrnoEISDIR:
		return experimentalsys.EISDIR
	case ErrnoENOTDIR:
		return experimentalsys.ENOTDIR
	case ErrnoEXDEV:
		return experimentalsys.ENOTSUP
	case ErrnoEINVAL:
		return experimentalsys.EINVAL
	case ErrnoEBADF:
		return experimentalsys.EBADF
	case ErrnoENOSPC:
		return experimentalsys.EIO
	case ErrnoEROFS:
		return experimentalsys.EROFS
	case ErrnoEIO:
		return experimentalsys.EIO
	default:
		return experimentalsys.EIO
	}
}

// SysToErrno maps wazero experimental/sys.Errno to pythonruntime.Errno.
func SysToErrno(e experimentalsys.Errno) Errno {
	switch e {
	case 0:
		return ErrnoSuccess
	case experimentalsys.ENOENT:
		return ErrnoENOENT
	case experimentalsys.EEXIST:
		return ErrnoEEXIST
	case experimentalsys.EACCES:
		return ErrnoEACCES
	case experimentalsys.EPERM:
		return ErrnoEPERM
	case experimentalsys.EISDIR:
		return ErrnoEISDIR
	case experimentalsys.ENOTDIR:
		return ErrnoENOTDIR
	case experimentalsys.ENOTSUP:
		return ErrnoEXDEV
	case experimentalsys.EINVAL:
		return ErrnoEINVAL
	case experimentalsys.EBADF:
		return ErrnoEBADF
	case experimentalsys.EROFS:
		return ErrnoEROFS
	default:
		return ErrnoEIO
	}
}

func statInfoToSys(st StatInfo) sys.Stat_t {
	mode := st.Mode
	if st.IsDir {
		mode |= fs.ModeDir
	}
	if st.IsSymlink {
		mode |= fs.ModeSymlink
	}
	mtim := st.ModTime.UnixNano()
	return sys.Stat_t{
		Dev:   1,
		Ino:   st.Inode,
		Mode:  mode,
		Nlink: 1,
		Size:  st.Size,
		Atim:  mtim,
		Mtim:  mtim,
		Ctim:  mtim,
	}
}

func cleanGuestRelPath(p string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return "."
	}
	return cleaned
}

// WorkspaceSysFSConfig configures a WorkspaceSysFS bridge mounted into wazero.
type WorkspaceSysFSConfig struct {
	Context     context.Context
	ExecutionID string
	Namespace   Namespace
	RootID      string
	Branch      string
	Adapter     WorkspaceAdapter
	Observer    OperationObserver
	Latch       *StopLatch
	Meter       *WorkMeter
}

// WorkspaceSysFS implements wazero's experimental/sys.FS, routing every guest
// filesystem syscall through OperationObserver.Admit -> WorkspaceAdapter ->
// OperationObserver.RecordOutcome and wiring DecisionStop into StopLatch.
type WorkspaceSysFS struct {
	experimentalsys.UnimplementedFS

	ctx         context.Context
	executionID string
	namespace   Namespace
	rootID      string
	branch      string
	adapter     WorkspaceAdapter
	observer    OperationObserver
	latch       *StopLatch
	meter       *WorkMeter
	opSeq       atomic.Uint64
}

// NewWorkspaceSysFS constructs a wazero sys.FS backed by WorkspaceAdapter and
// OperationObserver.
func NewWorkspaceSysFS(cfg WorkspaceSysFSConfig) *WorkspaceSysFS {
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = NamespaceWorkspace
	}
	return &WorkspaceSysFS{
		ctx:         ctx,
		executionID: cfg.ExecutionID,
		namespace:   ns,
		rootID:      cfg.RootID,
		branch:      cfg.Branch,
		adapter:     cfg.Adapter,
		observer:    cfg.Observer,
		latch:       cfg.Latch,
		meter:       cfg.Meter,
	}
}

func (w *WorkspaceSysFS) admit(family EffectFamily, operation, relPath string, args map[string]any) (string, experimentalsys.Errno) {
	if w.latch != nil && w.latch.Stopped() {
		return "", experimentalsys.EACCES
	}
	seq := w.opSeq.Add(1)
	opID := fmt.Sprintf("%s-fs-%d", w.executionID, seq)
	if w.observer == nil {
		return opID, 0
	}

	op := ReachedOperation{
		OperationID: opID,
		ExecutionID: w.executionID,
		Family:      family,
		Operation:   operation,
		Target: CanonicalTarget{
			Namespace: w.namespace,
			RootID:    w.rootID,
			Branch:    w.branch,
			RelPath:   relPath,
		},
		Args:         args,
		HostWorkCost: 1,
	}
	dec, err := w.observer.Admit(w.ctx, op)
	if err != nil {
		if w.ctx.Err() != nil && w.latch != nil {
			w.latch.Trigger(TerminalStop{
				Code:        StopCancelled,
				Reason:      err.Error(),
				OperationID: opID,
			})
		}
		return opID, experimentalsys.EIO
	}

	switch dec.Kind {
	case DecisionAllow:
		return opID, 0
	case DecisionDeny:
		if dec.Errno == ErrnoSuccess {
			return opID, experimentalsys.EACCES
		}
		return opID, ErrnoToSys(dec.Errno)
	case DecisionStop:
		stop := TerminalStop{
			Code:        StopEffectNotAllowed,
			Reason:      dec.Reason,
			OperationID: opID,
		}
		if dec.Stop != nil {
			stop = *dec.Stop
			if stop.OperationID == "" {
				stop.OperationID = opID
			}
		}
		if w.latch != nil {
			w.latch.Trigger(stop)
		}
		return opID, experimentalsys.EACCES
	default:
		// Unresolved hold or unknown decision fails closed.
		return opID, experimentalsys.EACCES
	}
}

func (w *WorkspaceSysFS) recordOutcome(opID string, errno Errno, bytes int64, hostWork uint64) {
	if hostWork > 0 && w.meter != nil {
		w.meter.ChargeHostWork(hostWork, opID)
	}
	if w.observer != nil && opID != "" {
		w.observer.RecordOutcome(w.ctx, opID, OperationOutcome{
			Succeeded:        errno == ErrnoSuccess,
			Errno:            errno,
			BytesTransferred: bytes,
			HostWorkCharged:  hostWork,
		})
	}
}

// OpenFile implements experimental/sys.FS.
func (w *WorkspaceSysFS) OpenFile(p string, flag experimentalsys.Oflag, perm fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	if w.adapter == nil {
		return nil, experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	openFlags := OflagToOpenFlags(flag)
	family := EffectFilesystemRead
	if openFlags&(OpenWrite|OpenCreate|OpenTruncate|OpenAppend) != 0 {
		family = EffectFilesystemWrite
	}

	opID, sysErr := w.admit(family, "open", rel, map[string]any{"flags": uint32(openFlags)})
	if sysErr != 0 {
		return nil, sysErr
	}

	gf, errno := w.adapter.Open(w.ctx, OpenRequest{
		Path:  rel,
		Flags: openFlags,
		Mode:  perm,
	})
	w.recordOutcome(opID, errno, 0, 1)
	if errno != ErrnoSuccess {
		return nil, ErrnoToSys(errno)
	}
	return &workspaceSysFile{
		fs:         w,
		file:       gf,
		relPath:    rel,
		flags:      openFlags,
		appendMode: openFlags&OpenAppend != 0,
	}, 0
}

// Stat implements experimental/sys.FS.
func (w *WorkspaceSysFS) Stat(p string) (sys.Stat_t, experimentalsys.Errno) {
	if w.adapter == nil {
		return sys.Stat_t{}, experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemMetadata, "stat", rel, nil)
	if sysErr != 0 {
		return sys.Stat_t{}, sysErr
	}
	st, errno := w.adapter.Stat(w.ctx, rel)
	w.recordOutcome(opID, errno, 0, 1)
	if errno != ErrnoSuccess {
		return sys.Stat_t{}, ErrnoToSys(errno)
	}
	return statInfoToSys(st), 0
}

// Lstat implements experimental/sys.FS.
func (w *WorkspaceSysFS) Lstat(p string) (sys.Stat_t, experimentalsys.Errno) {
	if w.adapter == nil {
		return sys.Stat_t{}, experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemMetadata, "lstat", rel, nil)
	if sysErr != 0 {
		return sys.Stat_t{}, sysErr
	}
	st, errno := w.adapter.Lstat(w.ctx, rel)
	w.recordOutcome(opID, errno, 0, 1)
	if errno != ErrnoSuccess {
		return sys.Stat_t{}, ErrnoToSys(errno)
	}
	return statInfoToSys(st), 0
}

// Mkdir implements experimental/sys.FS.
func (w *WorkspaceSysFS) Mkdir(p string, perm fs.FileMode) experimentalsys.Errno {
	if w.adapter == nil {
		return experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemWrite, "mkdir", rel, nil)
	if sysErr != 0 {
		return sysErr
	}
	errno := w.adapter.Mkdir(w.ctx, rel, perm)
	w.recordOutcome(opID, errno, 0, 1)
	return ErrnoToSys(errno)
}

// Unlink implements experimental/sys.FS.
func (w *WorkspaceSysFS) Unlink(p string) experimentalsys.Errno {
	if w.adapter == nil {
		return experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemWrite, "unlink", rel, nil)
	if sysErr != 0 {
		return sysErr
	}
	errno := w.adapter.Unlink(w.ctx, rel)
	w.recordOutcome(opID, errno, 0, 1)
	return ErrnoToSys(errno)
}

// Rmdir implements experimental/sys.FS.
func (w *WorkspaceSysFS) Rmdir(p string) experimentalsys.Errno {
	if w.adapter == nil {
		return experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemWrite, "rmdir", rel, nil)
	if sysErr != 0 {
		return sysErr
	}
	errno := w.adapter.Rmdir(w.ctx, rel)
	w.recordOutcome(opID, errno, 0, 1)
	return ErrnoToSys(errno)
}

// Rename implements experimental/sys.FS.
func (w *WorkspaceSysFS) Rename(from, to string) experimentalsys.Errno {
	if w.adapter == nil {
		return experimentalsys.ENOSYS
	}
	relFrom := cleanGuestRelPath(from)
	relTo := cleanGuestRelPath(to)
	opID, sysErr := w.admit(EffectFilesystemWrite, "rename", relFrom, map[string]any{"to": relTo})
	if sysErr != 0 {
		return sysErr
	}
	errno := w.adapter.Rename(w.ctx, relFrom, relTo)
	w.recordOutcome(opID, errno, 0, 1)
	return ErrnoToSys(errno)
}

// Readlink implements experimental/sys.FS.
func (w *WorkspaceSysFS) Readlink(p string) (string, experimentalsys.Errno) {
	if w.adapter == nil {
		return "", experimentalsys.ENOSYS
	}
	rel := cleanGuestRelPath(p)
	opID, sysErr := w.admit(EffectFilesystemMetadata, "readlink", rel, nil)
	if sysErr != 0 {
		return "", sysErr
	}
	target, errno := w.adapter.Readlink(w.ctx, rel)
	w.recordOutcome(opID, errno, 0, 1)
	return target, ErrnoToSys(errno)
}

type workspaceSysFile struct {
	experimentalsys.UnimplementedFile

	fs         *WorkspaceSysFS
	file       GuestFile
	relPath    string
	flags      OpenFlags
	mu         sync.Mutex
	appendMode bool
	dirLoaded  bool
	dirEntries []experimentalsys.Dirent
	dirPos     int
}

func (f *workspaceSysFile) Dev() (uint64, experimentalsys.Errno) {
	return 1, 0
}

func (f *workspaceSysFile) Ino() (sys.Inode, experimentalsys.Errno) {
	st, errno := f.file.Stat()
	if errno != ErrnoSuccess {
		return 0, ErrnoToSys(errno)
	}
	return st.Inode, 0
}

func (f *workspaceSysFile) IsDir() (bool, experimentalsys.Errno) {
	st, errno := f.file.Stat()
	if errno != ErrnoSuccess {
		return false, ErrnoToSys(errno)
	}
	return st.IsDir, 0
}

func (f *workspaceSysFile) IsAppend() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendMode
}

func (f *workspaceSysFile) SetAppend(enable bool) experimentalsys.Errno {
	f.mu.Lock()
	f.appendMode = enable
	f.mu.Unlock()
	return 0
}

func (f *workspaceSysFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	st, errno := f.file.Stat()
	if errno != ErrnoSuccess {
		return sys.Stat_t{}, ErrnoToSys(errno)
	}
	return statInfoToSys(st), 0
}

func (f *workspaceSysFile) Read(buf []byte) (int, experimentalsys.Errno) {
	if f.fs.latch != nil && f.fs.latch.Stopped() {
		return 0, experimentalsys.EACCES
	}
	n, errno := f.file.Read(buf)
	if f.fs.meter != nil && n > 0 {
		f.fs.meter.ChargeHostWork(1, "")
	}
	return n, ErrnoToSys(errno)
}

func (f *workspaceSysFile) Write(buf []byte) (int, experimentalsys.Errno) {
	if f.fs.latch != nil && f.fs.latch.Stopped() {
		return 0, experimentalsys.EACCES
	}
	n, errno := f.file.Write(buf)
	if f.fs.meter != nil && n > 0 {
		f.fs.meter.ChargeHostWork(1, "")
	}
	return n, ErrnoToSys(errno)
}

func (f *workspaceSysFile) Seek(offset int64, whence int) (int64, experimentalsys.Errno) {
	if offset == 0 && whence == io.SeekStart {
		f.mu.Lock()
		f.dirLoaded = false
		f.dirEntries = nil
		f.dirPos = 0
		f.mu.Unlock()
	}
	pos, errno := f.file.Seek(offset, whence)
	return pos, ErrnoToSys(errno)
}

func (f *workspaceSysFile) Truncate(size int64) experimentalsys.Errno {
	if f.fs.latch != nil && f.fs.latch.Stopped() {
		return experimentalsys.EACCES
	}
	return ErrnoToSys(f.file.Truncate(size))
}

func (f *workspaceSysFile) Sync() experimentalsys.Errno {
	return ErrnoToSys(f.file.Sync())
}

func (f *workspaceSysFile) Datasync() experimentalsys.Errno {
	return ErrnoToSys(f.file.Sync())
}

func (f *workspaceSysFile) Readdir(n int) ([]experimentalsys.Dirent, experimentalsys.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirLoaded {
		opID, sysErr := f.fs.admit(EffectFilesystemMetadata, "readdir", f.relPath, nil)
		if sysErr != 0 {
			return nil, sysErr
		}
		entries, errno := f.fs.adapter.ReadDir(f.fs.ctx, f.relPath)
		f.fs.recordOutcome(opID, errno, 0, 1)
		if errno != ErrnoSuccess {
			return nil, ErrnoToSys(errno)
		}
		dirents := make([]experimentalsys.Dirent, 0, len(entries))
		for i, e := range entries {
			ft := e.Mode & fs.ModeType
			if e.IsDir {
				ft = fs.ModeDir
			} else if e.IsSymlink {
				ft = fs.ModeSymlink
			}
			dirents = append(dirents, experimentalsys.Dirent{
				Ino:  uint64(i + 1),
				Name: e.Name,
				Type: ft,
			})
		}
		f.dirEntries = dirents
		f.dirLoaded = true
		f.dirPos = 0
	}
	if f.dirPos >= len(f.dirEntries) {
		return nil, 0
	}
	end := len(f.dirEntries)
	if n > 0 && f.dirPos+n < end {
		end = f.dirPos + n
	}
	slice := f.dirEntries[f.dirPos:end]
	f.dirPos = end
	return slice, 0
}

func (f *workspaceSysFile) Close() experimentalsys.Errno {
	return ErrnoToSys(f.file.Close())
}

// NullDeviceFS implements experimental/sys.FS for /dev, exposing /dev/null as
// an isolated guest null device without any host /dev access.
type NullDeviceFS struct {
	experimentalsys.UnimplementedFS
}

func (NullDeviceFS) OpenFile(p string, _ experimentalsys.Oflag, _ fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	rel := cleanGuestRelPath(p)
	switch rel {
	case ".":
		return &nullDirFile{}, 0
	case "null":
		return &nullDeviceFile{}, 0
	default:
		return nil, experimentalsys.ENOENT
	}
}

func (NullDeviceFS) Stat(p string) (sys.Stat_t, experimentalsys.Errno) {
	rel := cleanGuestRelPath(p)
	switch rel {
	case ".":
		return sys.Stat_t{Dev: 2, Ino: 1, Mode: fs.ModeDir | 0o555, Nlink: 1}, 0
	case "null":
		return sys.Stat_t{Dev: 2, Ino: 2, Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, Nlink: 1}, 0
	default:
		return sys.Stat_t{}, experimentalsys.ENOENT
	}
}

func (n NullDeviceFS) Lstat(p string) (sys.Stat_t, experimentalsys.Errno) {
	return n.Stat(p)
}

func (NullDeviceFS) Mkdir(string, fs.FileMode) experimentalsys.Errno { return experimentalsys.EROFS }
func (NullDeviceFS) Unlink(string) experimentalsys.Errno             { return experimentalsys.EROFS }
func (NullDeviceFS) Rmdir(string) experimentalsys.Errno              { return experimentalsys.EROFS }
func (NullDeviceFS) Rename(string, string) experimentalsys.Errno     { return experimentalsys.EROFS }

type nullDirFile struct {
	experimentalsys.DirFile
	experimentalsys.UnimplementedFile
	read bool
}

func (d *nullDirFile) Dev() (uint64, experimentalsys.Errno)    { return 2, 0 }
func (d *nullDirFile) Ino() (sys.Inode, experimentalsys.Errno) { return 1, 0 }
func (d *nullDirFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	return sys.Stat_t{Dev: 2, Ino: 1, Mode: fs.ModeDir | 0o555, Nlink: 1}, 0
}
func (d *nullDirFile) Readdir(_ int) ([]experimentalsys.Dirent, experimentalsys.Errno) {
	if d.read {
		return nil, 0
	}
	d.read = true
	return []experimentalsys.Dirent{{Ino: 2, Name: "null", Type: fs.ModeDevice | fs.ModeCharDevice}}, 0
}
func (d *nullDirFile) Seek(offset int64, whence int) (int64, experimentalsys.Errno) {
	if offset == 0 && whence == io.SeekStart {
		d.read = false
		return 0, 0
	}
	return 0, experimentalsys.EINVAL
}
func (d *nullDirFile) Close() experimentalsys.Errno { return 0 }

type nullDeviceFile struct {
	experimentalsys.UnimplementedFile
}

func (nullDeviceFile) Dev() (uint64, experimentalsys.Errno)     { return 2, 0 }
func (nullDeviceFile) Ino() (sys.Inode, experimentalsys.Errno)  { return 2, 0 }
func (nullDeviceFile) IsDir() (bool, experimentalsys.Errno)     { return false, 0 }
func (nullDeviceFile) IsAppend() bool                           { return false }
func (nullDeviceFile) SetAppend(bool) experimentalsys.Errno     { return 0 }
func (nullDeviceFile) Read([]byte) (int, experimentalsys.Errno) { return 0, 0 }
func (nullDeviceFile) Write(p []byte) (int, experimentalsys.Errno) {
	return len(p), 0
}
func (nullDeviceFile) Seek(int64, int) (int64, experimentalsys.Errno) { return 0, 0 }
func (nullDeviceFile) Truncate(int64) experimentalsys.Errno           { return 0 }
func (nullDeviceFile) Sync() experimentalsys.Errno                    { return 0 }
func (nullDeviceFile) Datasync() experimentalsys.Errno                { return 0 }
func (nullDeviceFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	return sys.Stat_t{Dev: 2, Ino: 2, Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, Nlink: 1}, 0
}
func (nullDeviceFile) Close() experimentalsys.Errno { return 0 }

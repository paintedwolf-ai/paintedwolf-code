package pythonruntime

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)
type hostDirSysFS struct {
	experimentalsys.UnimplementedFS
	root     string
	readOnly bool
}

func newHostDirSysFS(root string, readOnly bool) *hostDirSysFS {
	return &hostDirSysFS{root: filepath.Clean(root), readOnly: readOnly}
}

func (f *hostDirSysFS) resolve(rel string) (string, experimentalsys.Errno) {
	cleaned := cleanGuestRelPath(rel)
	if cleaned == "." {
		return f.root, 0
	}
	joined := filepath.Clean(filepath.Join(f.root, filepath.FromSlash(cleaned)))
	within, err := filepath.Rel(f.root, joined)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", experimentalsys.EACCES
	}
	return joined, 0
}

func (f *hostDirSysFS) OpenFile(p string, flag experimentalsys.Oflag, perm fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	full, errno := f.resolve(p)
	if errno != 0 {
		return nil, errno
	}
	isWrite := flag&(experimentalsys.O_WRONLY|experimentalsys.O_RDWR|experimentalsys.O_CREAT|experimentalsys.O_TRUNC|experimentalsys.O_APPEND) != 0
	if f.readOnly && isWrite {
		return nil, experimentalsys.EROFS
	}
	osFlag := os.O_RDONLY
	switch flag & 0x3 {
	case experimentalsys.O_WRONLY:
		osFlag = os.O_WRONLY
	case experimentalsys.O_RDWR:
		osFlag = os.O_RDWR
	}
	if flag&experimentalsys.O_CREAT != 0 {
		osFlag |= os.O_CREATE
	}
	if flag&experimentalsys.O_EXCL != 0 {
		osFlag |= os.O_EXCL
	}
	if flag&experimentalsys.O_TRUNC != 0 {
		osFlag |= os.O_TRUNC
	}
	if flag&experimentalsys.O_APPEND != 0 {
		osFlag |= os.O_APPEND
	}
	// #nosec G304 -- full is confined inside f.root by resolve().
	of, err := os.OpenFile(full, osFlag, perm)
	if err != nil {
		return nil, osErrToSysErrno(err)
	}
	return &hostOSFile{f: of, readOnly: f.readOnly}, 0
}

func (f *hostDirSysFS) Stat(p string) (sys.Stat_t, experimentalsys.Errno) {
	full, errno := f.resolve(p)
	if errno != 0 {
		return sys.Stat_t{}, errno
	}
	info, err := os.Stat(full)
	if err != nil {
		return sys.Stat_t{}, osErrToSysErrno(err)
	}
	return osFileInfoToSysStat(info), 0
}

func (f *hostDirSysFS) Lstat(p string) (sys.Stat_t, experimentalsys.Errno) {
	full, errno := f.resolve(p)
	if errno != 0 {
		return sys.Stat_t{}, errno
	}
	info, err := os.Lstat(full)
	if err != nil {
		return sys.Stat_t{}, osErrToSysErrno(err)
	}
	return osFileInfoToSysStat(info), 0
}

func (f *hostDirSysFS) Mkdir(p string, perm fs.FileMode) experimentalsys.Errno {
	if f.readOnly {
		return experimentalsys.EROFS
	}
	full, errno := f.resolve(p)
	if errno != 0 {
		return errno
	}
	return osErrToSysErrno(os.Mkdir(full, perm))
}

func (f *hostDirSysFS) Rmdir(p string) experimentalsys.Errno {
	if f.readOnly {
		return experimentalsys.EROFS
	}
	full, errno := f.resolve(p)
	if errno != 0 {
		return errno
	}
	return osErrToSysErrno(os.Remove(full))
}

func (f *hostDirSysFS) Unlink(p string) experimentalsys.Errno {
	if f.readOnly {
		return experimentalsys.EROFS
	}
	full, errno := f.resolve(p)
	if errno != 0 {
		return errno
	}
	return osErrToSysErrno(os.Remove(full))
}

func (f *hostDirSysFS) Rename(from, to string) experimentalsys.Errno {
	if f.readOnly {
		return experimentalsys.EROFS
	}
	src, e1 := f.resolve(from)
	if e1 != 0 {
		return e1
	}
	dst, e2 := f.resolve(to)
	if e2 != 0 {
		return e2
	}
	return osErrToSysErrno(os.Rename(src, dst))
}

type hostOSFile struct {
	experimentalsys.UnimplementedFile
	f        *os.File
	readOnly bool
}

func (hf *hostOSFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	info, err := hf.f.Stat()
	if err != nil {
		return sys.Stat_t{}, osErrToSysErrno(err)
	}
	return osFileInfoToSysStat(info), 0
}

func (hf *hostOSFile) Read(buf []byte) (int, experimentalsys.Errno) {
	n, err := hf.f.Read(buf)
	if err == io.EOF {
		return n, 0
	}
	return n, osErrToSysErrno(err)
}

func (hf *hostOSFile) Pread(buf []byte, off int64) (int, experimentalsys.Errno) {
	n, err := hf.f.ReadAt(buf, off)
	if err == io.EOF {
		return n, 0
	}
	return n, osErrToSysErrno(err)
}

func (hf *hostOSFile) Write(buf []byte) (int, experimentalsys.Errno) {
	if hf.readOnly {
		return 0, experimentalsys.EROFS
	}
	n, err := hf.f.Write(buf)
	return n, osErrToSysErrno(err)
}

func (hf *hostOSFile) Pwrite(buf []byte, off int64) (int, experimentalsys.Errno) {
	if hf.readOnly {
		return 0, experimentalsys.EROFS
	}
	n, err := hf.f.WriteAt(buf, off)
	return n, osErrToSysErrno(err)
}

func (hf *hostOSFile) Seek(off int64, whence int) (int64, experimentalsys.Errno) {
	pos, err := hf.f.Seek(off, whence)
	return pos, osErrToSysErrno(err)
}

func (hf *hostOSFile) Truncate(size int64) experimentalsys.Errno {
	if hf.readOnly {
		return experimentalsys.EROFS
	}
	return osErrToSysErrno(hf.f.Truncate(size))
}

func (hf *hostOSFile) Sync() experimentalsys.Errno {
	if hf.readOnly {
		return 0
	}
	return osErrToSysErrno(hf.f.Sync())
}

func (hf *hostOSFile) Datasync() experimentalsys.Errno { return hf.Sync() }

func (hf *hostOSFile) Readdir(n int) ([]experimentalsys.Dirent, experimentalsys.Errno) {
	entries, err := hf.f.ReadDir(n)
	if err != nil && err != io.EOF {
		return nil, osErrToSysErrno(err)
	}
	out := make([]experimentalsys.Dirent, 0, len(entries))
	for _, de := range entries {
		out = append(out, experimentalsys.Dirent{
			Name: de.Name(),
			Type: de.Type(),
		})
	}
	return out, 0
}

func (hf *hostOSFile) Close() experimentalsys.Errno {
	return osErrToSysErrno(hf.f.Close())
}

func osFileInfoToSysStat(info fs.FileInfo) sys.Stat_t {
	mtim := info.ModTime().UnixNano()
	return sys.Stat_t{
		Dev:   1,
		Ino:   1,
		Mode:  info.Mode(),
		Nlink: 1,
		Size:  info.Size(),
		Atim:  mtim,
		Mtim:  mtim,
		Ctim:  mtim,
	}
}

func osErrToSysErrno(err error) experimentalsys.Errno {
	if err == nil {
		return 0
	}
	switch {
	case os.IsNotExist(err):
		return experimentalsys.ENOENT
	case os.IsExist(err):
		return experimentalsys.EEXIST
	case os.IsPermission(err):
		return experimentalsys.EACCES
	default:
		return experimentalsys.EIO
	}
}

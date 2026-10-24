//go:build darwin

package fspath

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var kernelPathBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, unix.PathMax+12)
		return &buf
	},
}

// canonicalizeExisting resolves aliases without opening file contents.
func canonicalizeExisting(path string) (string, bool) {
	if resolved, errno, ok := kernelPathErrno(path); ok {
		return resolved, true
	} else if errno == unix.ENOENT || errno == unix.ENOTDIR || errno == unix.ELOOP {
		return "", false
	}
	// Missing tails are resolved by CanonicalPath's parent walk. Rewalking
	// every existing ancestor here repeats that work for each missing component.
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return "", false
	}
	linked, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	dir, base := filepath.Split(linked)
	if canonicalDir, ok := kernelPath(filepath.Clean(dir)); ok {
		return filepath.Join(canonicalDir, base), true
	}
	return linked, true
}

// kernelPath requests only the vnode's full-path attribute.
func kernelPath(path string) (string, bool) {
	resolved, _, ok := kernelPathErrno(path)
	return resolved, ok
}

func kernelPathErrno(path string) (string, syscall.Errno, bool) {
	name, err := unix.BytePtrFromString(path)
	if err != nil {
		return "", unix.EINVAL, false
	}
	attrs := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_FULLPATH}
	bufPtr := kernelPathBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer kernelPathBufPool.Put(bufPtr)
	// #nosec G103 -- getattrlist writes into the caller-owned attribute buffer.
	//nolint:staticcheck // x/sys has no getattrlist wrapper; metadata-only identity must also work without cgo.
	_, _, errno := unix.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return "", errno, false
	}
	size := int(binary.NativeEndian.Uint32(buf[:4]))
	// Attribute offsets are relative to their reference field.
	start := 4 + int(binary.NativeEndian.Uint32(buf[4:8]))
	length := int(binary.NativeEndian.Uint32(buf[8:12]))
	if size < 12 || size > len(buf) || start < 12 || length < 2 || start > size-length || buf[start+length-1] != 0 {
		return "", 0, false
	}
	return string(buf[start : start+length-1]), 0, true
}

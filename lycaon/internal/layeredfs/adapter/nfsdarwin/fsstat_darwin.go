//go:build darwin

package nfsdarwin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	mntNowait = 2

	// fsiocSyncVolume is FSIOC_SYNC_VOLUME (_IOW('A', 1, uint32_t)) and fsctlSyncWait is
	// FSCTL_SYNC_WAIT, from xnu's bsd/sys/fsctl.h, which the SDK does not ship.
	fsiocSyncVolume = 0x80044101
	fsctlSyncWait   = 1 << 1

	viewExportSourcePrefix = "127.0.0.1:/views/"
)

// syncVolume writes back every dirty buffer of the one volume holding path and waits for it. It
// needs no privilege and touches no other filesystem, unlike sync(2).
func syncVolume(path string) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	arg := uint32(fsctlSyncWait)
	_, _, errno := syscall.Syscall6(syscall.SYS_FSCTL, uintptr(unsafe.Pointer(p)), fsiocSyncVolume, uintptr(unsafe.Pointer(&arg)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// flushMappedPages writes back dirty pages that any process holds in a shared mapping of f. Neither
// fsync nor a volume sync reaches those on this client; msync through a second mapping of the same
// file does. The mapping is never touched here, so no page fault can wait on the in-process server.
func flushMappedPages(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	mapped, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return err
	}
	syncErr := msyncMappedRegion(mapped)
	return errors.Join(syncErr, syscall.Munmap(mapped))
}

func mountTable() ([]syscall.Statfs_t, error) {
	n, err := syscall.Getfsstat(nil, mntNowait)
	if err != nil {
		return nil, err
	}
	buf := make([]syscall.Statfs_t, n+8)
	n, err = syscall.Getfsstat(buf, mntNowait)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// mountsWithin lists mount points at or below dir.
func mountsWithin(dir string) ([]string, error) {
	table, err := mountTable()
	if err != nil {
		return nil, err
	}
	clean := canonicalPath(dir)
	var out []string
	for i := range table {
		on := filepath.Clean(int8SliceToString(table[i].Mntonname[:]))
		if on == clean || strings.HasPrefix(on, clean+"/") {
			out = append(out, on)
		}
	}
	return out, nil
}

// deadViewMounts lists NFS view mounts below root owned by uid.
func deadViewMounts(root string, uid uint32) ([]string, error) {
	table, err := mountTable()
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range table {
		on := filepath.Clean(int8SliceToString(table[i].Mntonname[:]))
		if table[i].Owner != uid || int8SliceToString(table[i].Fstypename[:]) != "nfs" ||
			!strings.HasPrefix(int8SliceToString(table[i].Mntfromname[:]), viewExportSourcePrefix) ||
			!strings.HasPrefix(on, root+"/") {
			continue
		}
		out = append(out, on)
	}
	return out, nil
}

func msyncMappedRegion(mapped []byte) error {
	if len(mapped) == 0 {
		return nil
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_MSYNC,
		uintptr(unsafe.Pointer(&mapped[0])),
		uintptr(len(mapped)),
		uintptr(syscall.MS_SYNC),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func statDeviceID(path string) (int32, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, nil
	}
	return st.Dev, nil
}

func checkKernelMountTable(mountPath, expectedSource string) (found bool, matchesSource bool) {
	cleanTarget := canonicalPath(mountPath)
	if cleanTarget == "" {
		return false, false
	}
	n, err := syscall.Getfsstat(nil, mntNowait)
	if err != nil || n <= 0 {
		return false, false
	}
	buf := make([]syscall.Statfs_t, n+8)
	n, err = syscall.Getfsstat(buf, mntNowait)
	if err != nil || n <= 0 {
		return false, false
	}
	for i := 0; i < n && i < len(buf); i++ {
		mntOn := filepath.Clean(int8SliceToString(buf[i].Mntonname[:]))
		if mntOn != cleanTarget {
			continue
		}
		mntFrom := int8SliceToString(buf[i].Mntfromname[:])
		if expectedSource == "" || mntFrom == expectedSource {
			return true, true
		}
		return true, false
	}
	return false, false
}

func int8SliceToString(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func canonicalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	parent := filepath.Dir(abs)
	if resolvedParent, rErr := filepath.EvalSymlinks(parent); rErr == nil {
		return filepath.Clean(filepath.Join(resolvedParent, filepath.Base(abs)))
	}
	return filepath.Clean(abs)
}

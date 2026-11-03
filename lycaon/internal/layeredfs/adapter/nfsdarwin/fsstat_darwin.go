//go:build darwin

package nfsdarwin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const mntNowait = 2

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

//go:build !darwin

package nfsdarwin

import (
	"path/filepath"
	"strings"
)

func statDeviceID(path string) (int32, error) {
	return 0, nil
}

func checkKernelMountTable(mountPath, expectedSource string) (found bool, matchesSource bool) {
	return false, false
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
	return filepath.Clean(abs)
}

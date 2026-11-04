//go:build !darwin

package nfsdarwin

import (
	"os"
	"path/filepath"
	"strings"
)

func msyncMappedRegion(_ []byte) error {
	return nil
}

func statDeviceID(_ string) (int32, error) {
	return 0, nil
}

func checkKernelMountTable(_, _ string) (found bool, matchesSource bool) {
	return false, false
}

func canonicalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

func syncVolume(_ string) error { return nil }

func mountsWithin(_ string) ([]string, error) { return nil, nil }

func deadViewMounts(_ string, _ uint32) ([]string, error) { return nil, nil }

func flushMappedPages(_ *os.File, _ int64) error { return nil }

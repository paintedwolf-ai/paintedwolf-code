//go:build linux

package launchguard

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// moduleRoots is every root-controlled path the module view leaves executable
// beside the generation.
func moduleRoots(policy GenerationPolicy, interpreter string) []string {
	roots := append([]string{}, policy.LibraryRoots...)
	roots = append(roots, policy.SystemExecutables...)
	if interpreter != "" {
		roots = append(roots, interpreter)
	}
	return roots
}

// reopenImage opens the desktop image again inside the module view, where the
// entry executes it, and proves it is the inode the stage selected.
func reopenImage(policy GenerationPolicy, selected *os.File) (*os.File, error) {
	reopened, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, err
	}
	var a, b unix.Stat_t
	if err := unix.Fstat(int(selected.Fd()), &a); err != nil {
		_ = reopened.Close()
		return nil, err
	}
	if err := unix.Fstat(int(reopened.Fd()), &b); err != nil || a.Dev != b.Dev || a.Ino != b.Ino {
		_ = reopened.Close()
		return nil, errors.New("module view image differs from retained selection")
	}
	return reopened, nil
}

func readMountInfo(fd int, buf []byte) ([]byte, error) {
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		return buf[:0], err
	}
	buf = buf[:0]
	for {
		if len(buf) > maxMountTopologyBytes {
			return buf[:0], errors.New("mount topology exceeds its byte bound")
		}
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		end := min(cap(buf), maxMountTopologyBytes+1)
		n, err := unix.Read(fd, buf[len(buf):end])
		if n > 0 {
			buf = buf[:len(buf)+n]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			return buf, err
		}
	}
}

func hasMountOption(options, want string) bool {
	for len(options) > 0 {
		token, rest, found := strings.Cut(options, ",")
		if token == want {
			return true
		}
		if !found {
			break
		}
		options = rest
	}
	return false
}

//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// resolveAnchor returns the trust anchor and relative path for path. Only
// paths inside an explicit test anchor start there; everything else walks from
// "/" and requires every ancestor up to "/" to be root-controlled.
func resolveAnchor(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", errors.New("trusted path must be absolute and clean")
	}
	anchor := "/"
	rel := strings.TrimPrefix(path, "/")
	if trustAnchor != "/" && filepath.IsAbs(trustAnchor) && filepath.Clean(trustAnchor) == trustAnchor {
		if r, err := filepath.Rel(trustAnchor, path); err == nil && r != ".." && !strings.HasPrefix(r, "../") {
			anchor = trustAnchor
			if r == "." {
				rel = ""
			} else {
				rel = r
			}
		}
	}
	return anchor, rel, nil
}

func openAnchor(anchor string) (*os.File, error) {
	fd, err := unix.Open(anchor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), anchor)
	info, err := file.Stat()
	if err == nil {
		err = trustedObject(info)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%s: %w", anchor, err)
	}
	return file, nil
}

// openRootControlled walks from the trust anchor to path using chained
// openat(O_NOFOLLOW) descriptors and fstatting each opened descriptor.
func openRootControlled(path string, directory bool) (*os.File, error) {
	anchor, rel, err := resolveAnchor(path)
	if err != nil {
		return nil, err
	}
	base, err := openAnchor(anchor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = base.Close() }()
	return openControlledAt(base, rel, directory)
}

func openControlledAt(base *os.File, rel string, directory bool) (*os.File, error) {
	dupFD, err := unix.Dup(int(base.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(dupFD)
	current := os.NewFile(uintptr(dupFD), base.Name())
	if rel == "" {
		if !directory {
			_ = current.Close()
			return nil, errors.New("trusted path has an unexpected object type")
		}
		return current, nil
	}
	components := strings.Split(rel, "/")
	for i, component := range components {
		last := i == len(components)-1
		name := filepath.Join(current.Name(), component)
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if !last || directory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), component, flags, 0)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(fd), name)
		info, err := current.Stat()
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if (!last || directory) && !info.IsDir() || (last && !directory && !info.Mode().IsRegular()) {
			_ = current.Close()
			return nil, errors.New("trusted path has an unexpected object type")
		}
		if err := trustedObject(info); err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return current, nil
}

// ensureControlledDirectory opens path below base one component at a time,
// creating each missing directory 0755 and requiring every component, new or
// existing, to satisfy trusted. Creation is relative to the held parent, so a
// component replaced concurrently is refused rather than followed.
func ensureControlledDirectory(base *os.File, path string, trusted func(os.FileInfo) error) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("trusted directory must be absolute, clean and below the root")
	}
	parent, err := unix.Dup(int(base.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(parent)
	current := os.NewFile(uintptr(parent), base.Name())
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if err := unix.Mkdirat(int(current.Fd()), component, 0o755); err == nil {
			// The creator's umask must not decide the directory's mode.
			if err := unix.Fchmodat(int(current.Fd()), component, 0o755, unix.AT_SYMLINK_NOFOLLOW); err != nil && !errors.Is(err, unix.EOPNOTSUPP) {
				_ = current.Close()
				return nil, err
			}
		} else if !errors.Is(err, unix.EEXIST) {
			_ = current.Close()
			return nil, err
		}
		fd, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(fd), component)
		info, err := current.Stat()
		if err == nil {
			err = trusted(info)
		}
		if err != nil {
			_ = current.Close()
			return nil, err
		}
	}
	return current, nil
}

// trustedObject requires the object to be owned by root (trustedOwner), not a
// symlink, not sticky, and not group- or world-writable. Sticky world-writable
// directories such as /tmp are never exempted.
func trustedObject(info os.FileInfo) error {
	facts, ok := info.Sys().(*syscall.Stat_t)
	if !ok || facts.Uid != trustedOwner || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSticky != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("launch path is not exclusively root-controlled")
	}
	return nil
}

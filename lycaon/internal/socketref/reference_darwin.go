//go:build darwin

package socketref

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"golang.org/x/sys/unix"
)

func Platform() Adapter { return Adapter{Command: KernelPathname, Native: RetainedObject} }

// Open retains a hard link behind the engine's terminal control-plane floor.
// Cross-filesystem services cannot be represented and never fall back to path.
func Open(path string) (*Reference, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := configdir.Root()
	if err != nil {
		return nil, err
	}
	aliases := filepath.Join(root, "socket-references")
	if err := os.MkdirAll(aliases, 0700); err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	realAliases, err := filepath.EvalSymlinks(aliases)
	if err != nil || realAliases != filepath.Join(realRoot, "socket-references") {
		return nil, ErrUnbound
	}
	var rootStat unix.Stat_t
	if err := unix.Lstat(realAliases, &rootStat); err != nil || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Mode&0777 != 0700 || rootStat.Uid != uint32(os.Getuid()) {
		return nil, ErrUnbound
	}
	aliases = realAliases

	dir, err := os.MkdirTemp(aliases, "service-")
	if err != nil {
		return nil, err
	}
	guard, err := os.Open(dir)
	if err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	alias := filepath.Join(dir, "socket")
	if len(alias) >= 104 {
		_ = guard.Close()
		_ = os.Remove(dir)
		return nil, fmt.Errorf("retained local-service address exceeds the kernel limit")
	}
	if err := os.Link(resolved, alias); err != nil {
		_ = guard.Close()
		_ = os.Remove(dir)
		return nil, err
	}
	var socketStat, dirStat unix.Stat_t
	if err := unix.Lstat(alias, &socketStat); err != nil || socketStat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		_ = os.Remove(alias)
		_ = guard.Close()
		_ = os.Remove(dir)
		return nil, fmt.Errorf("retained local service is not a socket: %v", err)
	}
	if err := unix.Fstat(int(guard.Fd()), &dirStat); err != nil {
		_ = os.Remove(alias)
		_ = guard.Close()
		_ = os.Remove(dir)
		return nil, err
	}
	release := func() error {
		var currentDir, currentSocket unix.Stat_t
		if unix.Lstat(dir, &currentDir) != nil || currentDir.Dev != dirStat.Dev || currentDir.Ino != dirStat.Ino {
			return ErrUnbound
		}
		if err := unix.Lstat(alias, &currentSocket); err != nil {
			return err
		}
		if currentSocket.Dev != socketStat.Dev || currentSocket.Ino != socketStat.Ino {
			return ErrUnbound
		}
		return errors.Join(os.Remove(alias), os.Remove(dir))
	}
	return &Reference{file: guard, path: resolved, release: release}, nil
}

func descriptorPath(file *os.File) string { return filepath.Join(file.Name(), "socket") }

func duplicate(*os.File) (*os.File, error) { return nil, ErrUnbound }

func sameObject(a, b *Reference) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a == b {
		return a.file != nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if a.file == nil || b.file == nil {
		return false
	}
	var first, second unix.Stat_t
	if unix.Lstat(descriptorPath(a.file), &first) != nil || unix.Lstat(descriptorPath(b.file), &second) != nil {
		return false
	}
	return first.Dev == second.Dev && first.Ino == second.Ino
}

func matchesPath(ref *Reference, path string) bool {
	ref.mu.RLock()
	defer ref.mu.RUnlock()
	if ref.file == nil {
		return false
	}
	var held, current unix.Stat_t
	if unix.Lstat(descriptorPath(ref.file), &held) != nil || unix.Stat(path, &current) != nil {
		return false
	}
	return held.Dev == current.Dev && held.Ino == current.Ino
}

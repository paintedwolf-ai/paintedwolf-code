//go:build linux

package packagegeneration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Source traversal starts at the fixed trust anchor. Every component is opened
// without following links, checked, and retained until its child is opened.
func (l layout) runtimeSourceDirectory(name string, snapshot *runtimeSnapshot) (*os.File, error) {
	relative, err := filepath.Rel(l.anchor, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, errors.New("runtime capture: source outside trust anchor")
	}
	current, err := os.OpenFile(l.anchor, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := l.recordRuntimeCustody(l.anchor, current, snapshot); err != nil {
		_ = current.Close()
		return nil, err
	}
	physical := l.anchor
	if relative == "." {
		return current, nil
	}
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		next, err := openRuntimeAt(current, component, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		physical = filepath.Join(physical, component)
		if err := l.recordRuntimeCustody(physical, next, snapshot); err != nil {
			_ = next.Close()
			_ = current.Close()
			return nil, err
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func openRuntimeAt(root *os.File, relative string, flags int) (*os.File, error) {
	fd, err := unix.Openat2(int(root.Fd()), relative, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), relative), nil
}

func (l layout) recordRuntimeCustody(name string, file *os.File, snapshot *runtimeSnapshot) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := controlledObject(name, info, l.owner); err != nil {
		return err
	}
	facts, err := runtimeFacts(info)
	if err != nil {
		return err
	}
	if previous, exists := snapshot.Custody[name]; exists && previous != facts {
		return errors.New("runtime capture: source ancestry changed during observation")
	}
	if _, exists := snapshot.Custody[name]; !exists {
		if err := snapshot.observationRoom(1); err != nil {
			return err
		}
	}
	snapshot.Custody[name] = facts
	return nil
}

func runtimeReadlink(file *os.File) (string, error) {
	// One extra byte distinguishes a full valid path from truncated provenance.
	raw := make([]byte, 4096)
	count, err := unix.Readlinkat(int(file.Fd()), "", raw)
	if err != nil {
		return "", err
	}
	if count == len(raw) || !runtimeText(string(raw[:count]), 4095) {
		return "", errors.New("runtime capture: invalid bounded source link")
	}
	return string(raw[:count]), nil
}

//go:build darwin || linux

package layeredfs

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type osLowerProvider struct {
	rootFile  *os.File
	hook      func(rel, comp string)
	xattrHook func(rel string) error
}

func newOSLowerProvider(rootPath string, hook func(rel, comp string), xattrHook func(rel string) error) (*osLowerProvider, error) {
	f, err := openDirNoFollow(rootPath)
	if err != nil {
		return nil, err
	}
	return &osLowerProvider{
		rootFile:  f,
		hook:      hook,
		xattrHook: xattrHook,
	}, nil
}

func (p *osLowerProvider) close() error {
	if p == nil || p.rootFile == nil {
		return nil
	}
	return p.rootFile.Close()
}

func (p *osLowerProvider) openParentAndName(rel string) (*os.File, string, error) {
	rel = cleanRelPath(rel)
	if rel == "." {
		dupFD, err := unix.Dup(int(p.rootFile.Fd()))
		if err != nil {
			return nil, ".", err
		}
		return os.NewFile(uintptr(dupFD), "."), ".", nil
	}
	parts := strings.Split(rel, "/")
	dupFD, err := unix.Dup(int(p.rootFile.Fd()))
	if err != nil {
		return nil, "", err
	}
	cur := os.NewFile(uintptr(dupFD), ".")
	for i := 0; i < len(parts)-1; i++ {
		comp := parts[i]
		if comp == "" || comp == "." || comp == ".." {
			_ = cur.Close()
			return nil, "", ErrPathEscape
		}
		if p.hook != nil {
			p.hook(rel, comp)
		}
		nextFD, openErr := unix.Openat(int(cur.Fd()), comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = cur.Close()
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, "", ErrSymlinkEscape
			}
			return nil, "", openErr
		}
		cur = os.NewFile(uintptr(nextFD), comp)
	}
	last := parts[len(parts)-1]
	if last == "" || last == "." || last == ".." {
		_ = cur.Close()
		return nil, "", ErrPathEscape
	}
	if p.hook != nil {
		p.hook(rel, last)
	}
	return cur, last, nil
}

func (p *osLowerProvider) Lstat(rel string) (LowerEntry, error) {
	parent, name, err := p.openParentAndName(rel)
	if err != nil {
		return LowerEntry{}, err
	}
	defer func() { _ = parent.Close() }()

	var st unix.Stat_t
	flag := unix.AT_SYMLINK_NOFOLLOW
	if name == "." {
		flag = 0
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &st, flag); err != nil {
		return LowerEntry{}, err
	}
	entry := statToLowerEntry(st)
	if entry.Kind == EntrySymlink {
		buf := make([]byte, unix.PathMax)
		n, lErr := unix.Readlinkat(int(parent.Fd()), name, buf)
		if lErr != nil {
			return LowerEntry{}, lErr
		}
		entry.LinkTarget = string(buf[:n])
	}
	return entry, nil
}

func (p *osLowerProvider) Readlink(rel string) (string, error) {
	parent, name, err := p.openParentAndName(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = parent.Close() }()
	if name == "." {
		return "", unix.EINVAL
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(int(parent.Fd()), name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func (p *osLowerProvider) OpenRead(rel string) (ReadSeekCloser, LowerEntry, error) {
	parent, name, err := p.openParentAndName(rel)
	if err != nil {
		return nil, LowerEntry{}, err
	}
	defer func() { _ = parent.Close() }()
	if name == "." {
		return nil, LowerEntry{}, unix.EISDIR
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, LowerEntry{}, ErrSymlinkEscape
		}
		return nil, LowerEntry{}, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, LowerEntry{}, err
	}
	entry := statToLowerEntry(st)
	if entry.Kind != EntryRegular {
		_ = f.Close()
		if entry.Kind == EntryDirectory {
			return nil, LowerEntry{}, unix.EISDIR
		}
		return nil, LowerEntry{}, ErrUnsupportedOperation
	}
	return &osFileReadHandle{file: f}, entry, nil
}

func (p *osLowerProvider) ReadDir(rel string) ([]LowerDirEntry, error) {
	parent, name, err := p.openParentAndName(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	var dirFile *os.File
	if name == "." {
		// A fresh open, never a dup: a dup shares the root descriptor's read offset, so only the
		// first listing of the root would see any entries.
		fd, oErr := unix.Openat(int(parent.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if oErr != nil {
			return nil, oErr
		}
		dirFile = os.NewFile(uintptr(fd), ".")
	} else {
		fd, oErr := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if oErr != nil {
			if errors.Is(oErr, unix.ELOOP) {
				return nil, ErrSymlinkEscape
			}
			return nil, oErr
		}
		dirFile = os.NewFile(uintptr(fd), name)
	}
	defer func() { _ = dirFile.Close() }()

	osEntries, err := dirFile.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]LowerDirEntry, 0, len(osEntries))
	for _, de := range osEntries {
		var st unix.Stat_t
		if err := unix.Fstatat(int(dirFile.Fd()), de.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return nil, err
		}
		le := statToLowerEntry(st)
		out = append(out, LowerDirEntry{
			Name: de.Name(),
			Kind: le.Kind,
			Mode: le.Mode,
			Ino:  le.Ino,
		})
	}
	return out, nil
}

func (p *osLowerProvider) ListXattr(rel string) (map[string][]byte, error) {
	parent, name, err := p.openParentAndName(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	if p.xattrHook != nil {
		if hErr := p.xattrHook(cleanRelPath(rel)); hErr != nil {
			return nil, hErr
		}
	}
	return listXattrAt(int(parent.Fd()), name)
}

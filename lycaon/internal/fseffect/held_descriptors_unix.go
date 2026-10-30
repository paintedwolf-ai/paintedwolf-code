//go:build darwin || linux

package fseffect

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// Held descriptors: an opened root and the parent directory of one target,
// walked without following symlinks and cached by device and inode.

type rootCacheEntry struct {
	rawPath   string
	canonical string
	dev       uint64
	ino       uint64
}

type sharedParentDir struct {
	rawRoot  string
	rel      string
	rootPath string
	rootDev  uint64
	rootIno  uint64
	dev      uint64
	ino      uint64
	fd       int
	refs     atomic.Int32
}

func (s *sharedParentDir) release() {
	if s.refs.Add(-1) == 0 && s.fd >= 0 {
		_ = unix.Close(s.fd)
		s.fd = -1
	}
}

var (
	rootCache struct {
		mu      sync.RWMutex
		entries [8]rootCacheEntry
		next    int
	}
	sharedParentCache struct {
		mu  sync.RWMutex
		dir *sharedParentDir
	}
)

type openedRoot struct {
	readOpens atomic.Int64
	path      string
	fd        int
	dev       uint64
	ino       uint64
}

type openedParent struct {
	rootPath string
	rootDev  uint64
	rootIno  uint64
	dev      uint64
	ino      uint64
	fd       int
	name     string
	rel      string
	shared   *sharedParentDir
}

type targetAt struct{ parent *openedParent }

func openRoot(path string) (*openedRoot, error) {
	return openRootFlags(path, unix.O_RDONLY)
}

func openRootFlags(path string, accessFlags int) (*openedRoot, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		if fd, err := unix.Open(clean, accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0); err == nil {
			var st unix.Stat_t
			if unix.Fstat(fd, &st) == nil {
				rootCache.mu.RLock()
				var canonical string
				for i := range rootCache.entries {
					e := &rootCache.entries[i]
					if (e.rawPath == clean || e.canonical == clean) && e.dev == deviceID(&st) && e.ino == st.Ino {
						canonical = e.canonical
						break
					}
				}
				rootCache.mu.RUnlock()
				if canonical != "" {
					return &openedRoot{path: canonical, fd: fd, dev: deviceID(&st), ino: st.Ino}, nil
				}
			}
			_ = unix.Close(fd)
		}
		if root, err := walkRootNoFollow(clean, clean, accessFlags); err == nil {
			return root, nil
		}
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve effect root: %w", err)
	}
	return walkRootNoFollow(clean, canonical, accessFlags)
}

func walkRootNoFollow(rawPath, canonical string, accessFlags int) (*openedRoot, error) {
	fd, err := unix.Open(string(filepath.Separator), accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open effect root: %w", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component,
			accessFlags|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: effect root component %q", ErrSymlink, component)
			}
			return nil, fmt.Errorf("open effect root component %q: %w", component, openErr)
		}
		fd = next
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) == nil {
		rootCache.mu.Lock()
		stored := false
		for i := range rootCache.entries {
			if (rootCache.entries[i].rawPath == rawPath && rootCache.entries[i].canonical == canonical) || rootCache.entries[i].canonical == "" {
				rootCache.entries[i] = rootCacheEntry{rawPath: rawPath, canonical: canonical, dev: deviceID(&st), ino: st.Ino}
				stored = true
				break
			}
		}
		if !stored {
			rootCache.entries[rootCache.next] = rootCacheEntry{rawPath: rawPath, canonical: canonical, dev: deviceID(&st), ino: st.Ino}
			rootCache.next = (rootCache.next + 1) % len(rootCache.entries)
		}
		rootCache.mu.Unlock()
	}
	return &openedRoot{path: canonical, fd: fd, dev: deviceID(&st), ino: st.Ino}, nil
}

func (r *openedRoot) close() {
	if r.fd >= 0 {
		_ = unix.Close(r.fd)
		r.fd = -1
	}
}

func openParent(loc Location, create bool, mode os.FileMode) (*openedParent, error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(clean.Rel), "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 {
		parentRel := filepath.FromSlash(strings.Join(parts[:len(parts)-1], "/"))
		sharedParentCache.mu.RLock()
		d := sharedParentCache.dir
		if d != nil && d.rawRoot == clean.Root && d.rel == parentRel && d.fd >= 0 {
			d.refs.Add(1)
			sharedParentCache.mu.RUnlock()
			var st unix.Stat_t
			if unix.Fstatat(unix.AT_FDCWD, filepath.Join(d.rootPath, d.rel), &st, unix.AT_SYMLINK_NOFOLLOW) == nil && deviceID(&st) == d.dev && st.Ino == d.ino {
				return &openedParent{
					rootPath: d.rootPath, rootDev: d.rootDev, rootIno: d.rootIno,
					dev: d.dev, ino: d.ino, fd: d.fd, name: name, rel: d.rel, shared: d,
				}, nil
			}
			d.release()
		} else {
			sharedParentCache.mu.RUnlock()
		}
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return nil, err
	}
	if len(parts) == 1 {
		return &openedParent{rootPath: root.path, rootDev: root.dev, rootIno: root.ino, dev: root.dev, ino: root.ino, fd: root.fd, name: name}, nil
	}
	currentFD := root.fd
	ownsCurrent := false
	closeCurrent := func() {
		if ownsCurrent {
			_ = unix.Close(currentFD)
		}
	}
	parentRel := ""
	for _, component := range parts[:len(parts)-1] {
		if component == "" || component == "." || component == ".." {
			closeCurrent()
			root.close()
			return nil, fmt.Errorf("%w: component %q", ErrInvalidPath, component)
		}
		created := false
		nextFD, openErr := unix.Openat(currentFD, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(currentFD, component, uint32(mode.Perm()))
			if mkdirErr == nil {
				created = true
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				closeCurrent()
				root.close()
				return nil, fmt.Errorf("create effect directory %q: %w", component, mkdirErr)
			}
			nextFD, openErr = unix.Openat(currentFD, component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			closeCurrent()
			root.close()
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: %q", ErrSymlink, component)
			}
			// PathError so os.IsNotExist recognizes a missing component.
			return nil, &fs.PathError{Op: "open effect directory", Path: component, Err: openErr}
		}
		if created {
			if syncErr := fsyncFD(currentFD); syncErr != nil {
				_ = unix.Close(nextFD)
				closeCurrent()
				root.close()
				return nil, fmt.Errorf("sync effect directory parent: %w", syncErr)
			}
		}
		closeCurrent()
		parentRel = filepath.Join(parentRel, component)
		currentFD = nextFD
		ownsCurrent = true
	}
	rootPath, rootDev, rootIno := root.path, root.dev, root.ino
	root.close()
	var st unix.Stat_t
	if unix.Fstat(currentFD, &st) == nil && st.Ino != 0 {
		d := &sharedParentDir{
			rawRoot: clean.Root, rel: parentRel,
			rootPath: rootPath, rootDev: rootDev, rootIno: rootIno,
			dev: deviceID(&st), ino: st.Ino, fd: currentFD,
		}
		d.refs.Store(2)
		sharedParentCache.mu.Lock()
		old := sharedParentCache.dir
		sharedParentCache.dir = d
		sharedParentCache.mu.Unlock()
		if old != nil {
			old.release()
		}
		return &openedParent{
			rootPath: rootPath, rootDev: rootDev, rootIno: rootIno,
			dev: deviceID(&st), ino: st.Ino, fd: currentFD, name: name, rel: parentRel, shared: d,
		}, nil
	}
	return &openedParent{rootPath: rootPath, rootDev: rootDev, rootIno: rootIno, fd: currentFD, name: name, rel: parentRel}, nil
}

func (p *openedParent) close() {
	if p.shared != nil {
		p.shared.release()
		p.shared = nil
		p.fd = -1
		return
	}
	if p.fd >= 0 {
		_ = unix.Close(p.fd)
		p.fd = -1
	}
}

func (t targetAt) Open() (*os.File, error) {
	fd, err := unix.Openat(t.parent.fd, t.parent.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, t.parent.name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(t.parent.rootPath, t.parent.rel, t.parent.name)), nil
}

func (t targetAt) Lstat() (os.FileInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(t.parent.fd, t.parent.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("%w: %s", ErrSymlink, t.parent.name)
	}
	f, err := t.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}

// deviceID widens Stat_t.Dev, which is int32 on Darwin and uint64 on Linux.
func deviceID(st *unix.Stat_t) uint64 {
	return uint64(st.Dev)
}

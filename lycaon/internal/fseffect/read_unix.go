//go:build darwin || linux

package fseffect

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// maxReadLinkHops bounds symlink resolution work.
const maxReadLinkHops = 16

// readLinkBufBytes sizes one Readlinkat; a target this long is refused, not truncated.
const readLinkBufBytes = unix.PathMax

// readWalk holds the verified directory chain and cannot pop past its root.
type readWalk struct {
	dirs []*os.File
}

func (w *readWalk) top() *os.File { return w.dirs[len(w.dirs)-1] }

func (w *readWalk) push(f *os.File) { w.dirs = append(w.dirs, f) }

// pop drops the deepest directory, reporting false at the root.
func (w *readWalk) pop() bool {
	if len(w.dirs) <= 1 {
		return false
	}
	_ = w.dirs[len(w.dirs)-1].Close()
	w.dirs = w.dirs[:len(w.dirs)-1]
	return true
}

// rewind returns to the root, for an absolute link target re-walked from there.
func (w *readWalk) rewind() {
	for w.pop() {
	}
}

func (w *readWalk) close() {
	for _, f := range w.dirs {
		_ = f.Close()
	}
	w.dirs = nil
}

func lexicalRelUnder(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// relUnderRoot maps a canonical in-root target to a relative path.
func relUnderRoot(root, target string) (string, bool) {
	prefix := root + string(filepath.Separator)
	if root == string(filepath.Separator) {
		prefix = root
	}
	if strings.HasPrefix(target, prefix) {
		return target[len(prefix):], true
	}
	if target == root {
		return ".", true
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false
	}
	return lexicalRelUnder(root, resolved)
}

func readLinkAt(dirFD int, name string) (string, error) {
	buf := make([]byte, readLinkBufBytes)
	n, err := unix.Readlinkat(dirFD, name, buf)
	if err != nil {
		return "", &fs.PathError{Op: "read effect link", Path: name, Err: err}
	}
	if n <= 0 || n >= len(buf) {
		return "", fmt.Errorf("%w: %s has an unreadable target", ErrSymlink, name)
	}
	return string(buf[:n]), nil
}

// ReadRoot holds an admitted root open, so every read beneath it resolves from
// one descriptor instead of re-walking the root's own path. It is safe for
// concurrent use.
type ReadRoot struct {
	root      *openedRoot
	mu        sync.Mutex
	lastDir   string
	lastDirFD int
}

// OpenReadRoot opens the directory at path.
func OpenReadRoot(path string) (*ReadRoot, error) {
	clean, err := cleanReadLocation(Location{Root: path, Rel: "."})
	if err != nil {
		return nil, err
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return nil, err
	}
	return &ReadRoot{root: root, lastDirFD: -1}, nil
}

// Path is the root's canonical path.
func (r *ReadRoot) Path() string { return r.root.path }

// Open opens rel beneath the root, following symlinks that stay inside it.
func (r *ReadRoot) Open(rel string) (*os.File, error) {
	clean, err := cleanReadLocation(Location{Root: r.root.path, Rel: rel})
	if err != nil {
		return nil, err
	}
	if clean.Rel == "." {
		fd, err := unix.Dup(r.root.fd)
		if err != nil {
			return nil, fmt.Errorf("duplicate effect root: %w", err)
		}
		r.root.readOpens.Add(1)
		return os.NewFile(uintptr(fd), r.root.path), nil
	}
	return r.openReadContained(clean.Rel)
}

// Close releases the root descriptor; files it opened stay open.
func (r *ReadRoot) Close() error {
	r.mu.Lock()
	if r.lastDirFD >= 0 {
		_ = unix.Close(r.lastDirFD)
		r.lastDirFD = -1
		r.lastDir = ""
	}
	r.mu.Unlock()
	r.root.close()
	return nil
}

func openLeafReadAt(dirFD int, leaf string) (int, error) {
	return unix.Openat(dirFD, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

// openReadContained resolves every symlink hop beneath a held root descriptor.
func (r *ReadRoot) openReadContained(relPath string) (*os.File, error) {
	root := r.root
	slashed := filepath.ToSlash(relPath)
	if idx := strings.LastIndexByte(slashed, '/'); idx > 0 {
		dirRel := slashed[:idx]
		leaf := slashed[idx+1:]
		if leaf != "" && leaf != "." && leaf != ".." {
			r.mu.Lock()
			if r.lastDirFD >= 0 && r.lastDir == dirRel {
				fd, err := openLeafReadAt(r.lastDirFD, leaf)
				r.mu.Unlock()
				if err == nil {
					root.readOpens.Add(1)
					return os.NewFile(uintptr(fd), filepath.Join(root.path, filepath.FromSlash(relPath))), nil
				}
			} else {
				r.mu.Unlock()
			}
		}
	}
	parts := strings.Split(slashed, "/")
	if len(parts) > 0 {
		curFD := root.fd
		ownsCur := false
		ok := true
		for _, comp := range parts[:len(parts)-1] {
			if comp == "" || comp == "." || comp == ".." {
				ok = false
				break
			}
			nextFD, err := unix.Openat(curFD, comp, metadataOpenFlags(true)|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				ok = false
				break
			}
			root.readOpens.Add(1)
			if ownsCur {
				_ = unix.Close(curFD)
			}
			curFD = nextFD
			ownsCur = true
		}
		if ok {
			leaf := parts[len(parts)-1]
			if leaf != "" && leaf != "." && leaf != ".." {
				fd, err := openLeafReadAt(curFD, leaf)
				if err == nil {
					root.readOpens.Add(1)
				}
				if ownsCur {
					if err == nil {
						dirRel := strings.Join(parts[:len(parts)-1], "/")
						r.mu.Lock()
						if r.lastDirFD >= 0 {
							_ = unix.Close(r.lastDirFD)
						}
						r.lastDir = dirRel
						r.lastDirFD = curFD
						r.mu.Unlock()
					} else {
						_ = unix.Close(curFD)
					}
				}
				if err == nil {
					return os.NewFile(uintptr(fd), filepath.Join(root.path, filepath.FromSlash(relPath))), nil
				}
			} else if ownsCur {
				_ = unix.Close(curFD)
			}
		} else if ownsCur {
			_ = unix.Close(curFD)
		}
	}
	return openReadWalk(root, relPath)
}

func openReadWalk(root *openedRoot, relPath string) (*os.File, error) {
	return walkReadContained(root, relPath, true, func(dir *os.File, name string, _ *unix.Stat_t) (*os.File, error) {
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, &fs.PathError{Op: "open effect target", Path: name, Err: err}
		}
		root.readOpens.Add(1)
		return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
	})
}

// walkReadContained shares the same held-directory confinement for content and
// metadata operations. Only an explicit no-follow query receives link metadata.
func walkReadContained(root *openedRoot, relPath string, follow bool, visit func(*os.File, string, *unix.Stat_t) (*os.File, error)) (*os.File, error) {
	baseFD, err := unix.Dup(root.fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate effect root: %w", err)
	}
	root.readOpens.Add(1)
	w := &readWalk{}
	w.push(os.NewFile(uintptr(baseFD), root.path))
	defer w.close()

	pending := strings.Split(filepath.ToSlash(relPath), "/")
	hops := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			if !w.pop() {
				return nil, fmt.Errorf("%w: %q leaves the root", ErrSymlink, relPath)
			}
			continue
		}
		dir := w.top()
		var st unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			// PathError so os.IsNotExist recognizes a missing component.
			return nil, &fs.PathError{Op: "open effect path", Path: name, Err: err}
		}
		if len(pending) == 0 && !follow {
			return visit(dir, name, &st)
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			hops++
			if hops > maxReadLinkHops {
				return nil, fmt.Errorf("%w: %q exceeds %d links", ErrSymlink, relPath, maxReadLinkHops)
			}
			target, linkErr := readLinkAt(int(dir.Fd()), name)
			if linkErr != nil {
				return nil, linkErr
			}
			if filepath.IsAbs(target) {
				rel, ok := relUnderRoot(root.path, target)
				if !ok {
					return nil, fmt.Errorf("%w: %s resolves outside the root", ErrSymlink, name)
				}
				w.rewind()
				target = rel
			}
			pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			return visit(dir, name, &st)
		}
		fd, openErr := unix.Openat(int(dir.Fd()), name,
			metadataOpenFlags(true)|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, &fs.PathError{Op: "open effect directory", Path: name, Err: openErr}
		}
		root.readOpens.Add(1)
		w.push(os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)))
	}
	// A fully consumed path names a directory.
	var st unix.Stat_t
	if err := unix.Fstat(int(w.top().Fd()), &st); err != nil {
		return nil, err
	}
	return visit(w.top(), ".", &st)
}

// OpenRead opens a target beneath the root, following symlinks that stay inside it.
func OpenRead(loc Location) (*os.File, error) {
	root, err := OpenReadRoot(loc.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(loc.Rel)
}

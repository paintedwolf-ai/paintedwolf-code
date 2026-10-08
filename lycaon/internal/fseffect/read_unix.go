//go:build darwin || linux

package fseffect

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
	if rel, ok := lexicalRelUnder(root, target); ok {
		return rel, true
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
type ReadRoot struct{ root *openedRoot }

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
	return &ReadRoot{root: root}, nil
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
		fd, err := unix.Dup(int(r.root.file.Fd()))
		if err != nil {
			return nil, fmt.Errorf("duplicate effect root: %w", err)
		}
		return os.NewFile(uintptr(fd), r.root.path), nil
	}
	return openReadContained(r.root, clean.Rel)
}

// Close releases the root descriptor; files it opened stay open.
func (r *ReadRoot) Close() error {
	r.root.close()
	return nil
}

// openReadContained resolves every symlink hop beneath a held root descriptor.
func openReadContained(root *openedRoot, relPath string) (*os.File, error) {
	baseFD, err := unix.Dup(int(root.file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate effect root: %w", err)
	}
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
			// Nonblocking open allows file-type validation after a FIFO replacement.
			fd, openErr := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if openErr != nil {
				return nil, &fs.PathError{Op: "open effect target", Path: name, Err: openErr}
			}
			return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
		}
		fd, openErr := unix.Openat(int(dir.Fd()), name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, &fs.PathError{Op: "open effect directory", Path: name, Err: openErr}
		}
		w.push(os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)))
	}
	// A fully consumed path names a directory.
	fd, err := unix.Dup(int(w.top().Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate effect directory: %w", err)
	}
	return os.NewFile(uintptr(fd), w.top().Name()), nil
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

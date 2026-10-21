//go:build darwin || linux

package fseffect

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

// Canonical returns an existing descriptor-resolved native identity.
func (r *ReadRoot) Canonical(rel string) (string, error) {
	file, err := r.OpenExact(rel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}

// Readlink reads only a link's metadata through its held contained parent.
func (r *ReadRoot) Readlink(rel string, admit func(string) error) (string, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return "", err
	}
	split := strings.LastIndexByte(relative, byte(filepath.Separator))
	parentPath, name := ".", relative
	if split >= 0 {
		parentPath, name = relative[:split], relative[split+1:]
	}
	parent, err := r.OpenExact(parentPath)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	if admit != nil {
		if err := admit(filepath.Join(parent.Name(), name)); err != nil {
			return "", err
		}
	}
	return readLinkAt(int(parent.Fd()), name)
}

// OpenExact uses native OS component order through the same jailed read walk.
func (r *ReadRoot) OpenExact(rel string) (*os.File, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return nil, err
	}
	return openReadContained(r.root, relative)
}

// MetadataRoot holds descriptor-relative launch metadata authority. Entry opens
// require native execution permission; cwd opens require directory search only.
type MetadataRoot struct{ root *openedRoot }

func OpenMetadataRoot(path string) (*MetadataRoot, error) {
	clean, err := cleanReadLocation(Location{Root: path, Rel: "."})
	if err != nil {
		return nil, err
	}
	root, err := openRootFlags(clean.Root, metadataOpenFlags(true))
	if err != nil {
		return nil, err
	}
	return &MetadataRoot{root: root}, nil
}
func (r *MetadataRoot) Path() string { return r.root.path }
func (r *MetadataRoot) Close() error { r.root.close(); return nil }
func (r *MetadataRoot) OpenExact(rel string, directory bool) (*os.File, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return nil, err
	}
	return walkReadContained(r.root, relative, true, func(dir *os.File, name string, expected *unix.Stat_t) (*os.File, error) {
		fd, err := unix.Openat(int(dir.Fd()), name, metadataOpenFlags(directory)|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		var actual unix.Stat_t
		if err := unix.Fstat(fd, &actual); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if actual.Mode&unix.S_IFMT == unix.S_IFLNK || actual.Dev != expected.Dev || actual.Ino != expected.Ino {
			_ = unix.Close(fd)
			return nil, ErrSymlink
		}
		return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
	})
}

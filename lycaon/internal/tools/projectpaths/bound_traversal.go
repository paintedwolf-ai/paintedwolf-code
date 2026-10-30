package projectpaths

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func bindOpenDirectory(p BoundPath, root *fseffect.ReadRoot, ownsRoot bool, file *os.File) (*BoundDir, error) {
	fail := func(err error) (*BoundDir, error) {
		_ = file.Close()
		if ownsRoot {
			_ = root.Close()
		}
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	actual, err := root.DirectoryPath(file)
	if errors.Is(err, fseffect.ErrUnsupported) {
		actual, err = file.Name(), nil
	}
	if err != nil {
		return fail(err)
	}
	if p.admitRead != nil {
		if err := p.admitRead(actual); err != nil {
			return fail(err)
		}
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() {
		return fail(&fs.PathError{Op: "readdir", Path: p.Resolved.DisplayPath, Err: errors.New("not a directory")})
	}
	return &BoundDir{dir: p, root: root, ownsRoot: ownsRoot, file: file}, nil
}

// OpenChildDirectory retains one immediate child's directory under the same authority.
func (d *BoundDir) OpenChildDirectory(name string) (*BoundDir, error) {
	child, err := d.Child(name)
	if err != nil {
		return nil, err
	}
	file, err := d.root.OpenChildDirectory(d.file, name)
	if errors.Is(err, fseffect.ErrUnsupported) {
		return child.OpenDirWithRoot(d.root)
	}
	if err != nil {
		return nil, PathOperationError(child.Resolved.DisplayPath, err)
	}
	return bindOpenDirectory(child, d.root, false, file)
}

// DirectoryFile borrows the held descriptor; the caller must not close it.
func (d *BoundDir) DirectoryFile() *os.File { return d.file }

// ResetListing discards a failed borrowed observation before a live read.
func (d *BoundDir) ResetListing() error {
	if d.closed {
		return os.ErrClosed
	}
	if _, err := d.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	d.names, d.listed, d.listErr = nil, false, nil
	return nil
}

func (d *BoundDir) currentPath() (string, error) {
	if d.closed {
		return "", os.ErrClosed
	}
	if d.root == nil {
		return d.file.Name(), nil
	}
	path, err := d.root.DirectoryPath(d.file)
	if errors.Is(err, fseffect.ErrUnsupported) {
		return d.namedDirectoryPath()
	}
	return path, err
}

// AdmitDirectoryRead checks the held directory's current physical address and read floor.
func (d *BoundDir) AdmitDirectoryRead() error {
	actual, err := d.currentPath()
	if err == nil && d.dir.admitRead != nil {
		err = d.dir.admitRead(actual)
	}
	return PathOperationError(d.dir.Resolved.DisplayPath, err)
}

// namedDirectoryPath reopens through the bound authority when native handle paths are unavailable.
func (d *BoundDir) namedDirectoryPath() (string, error) {
	current, err := d.dir.OpenDir()
	if err != nil {
		return "", err
	}
	defer current.Close()
	expected, err := d.file.Stat()
	if err != nil {
		return "", err
	}
	actual, err := current.file.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(expected, actual) {
		return "", fseffect.ErrSymlink
	}
	return current.file.Name(), nil
}

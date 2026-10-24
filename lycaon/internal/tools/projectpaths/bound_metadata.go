package projectpaths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// BoundDir holds a bound directory and its root descriptor open while listing
// and inspecting its immediate entries.
type BoundDir struct {
	dir   BoundPath
	root  *fseffect.ReadRoot
	file  *os.File
	names []string
}

// OpenDir opens a bound directory and lists its entry names in unsigned byte order.
func (p BoundPath) OpenDir() (*BoundDir, error) {
	if p.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Cause: errors.New("null input is an empty stream, not a file")}
	}
	root, err := p.openRoot()
	if err != nil {
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	file, err := root.OpenExact(p.exactRelative)
	if err != nil {
		_ = root.Close()
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	if p.admitRead != nil {
		if err := p.admitRead(file.Name()); err != nil {
			_ = file.Close()
			_ = root.Close()
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	if !info.IsDir() {
		_ = file.Close()
		_ = root.Close()
		return nil, PathOperationError(p.Resolved.DisplayPath, &fs.PathError{Op: "readdir", Path: p.Resolved.DisplayPath, Err: errors.New("not a directory")})
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	slices.Sort(names)
	return &BoundDir{dir: p, root: root, file: file, names: names}, nil
}

// Names returns the sorted entry names in the held directory.
func (d *BoundDir) Names() []string { return d.names }

// Close releases the held directory and root descriptors.
func (d *BoundDir) Close() error {
	return errors.Join(d.file.Close(), d.root.Close())
}

// StatChild binds and stats one immediate entry of the held directory without following links.
func (d *BoundDir) StatChild(name string) (BoundPath, fseffect.EntryStat, error) {
	child := d.dir.Child(name)
	st, err := d.root.StatChildAt(d.file, child.exactRelative, name, child.admitEntry)
	if err != nil {
		return child, fseffect.EntryStat{}, PathOperationError(child.Resolved.DisplayPath, err)
	}
	return child, st, nil
}

// StatChildFollowed resolves a symlink child's target through the held root.
func (d *BoundDir) StatChildFollowed(child BoundPath) (fseffect.EntryStat, error) {
	st, err := d.root.Stat(child.exactRelative, true, child.admitRead)
	if err != nil {
		return fseffect.EntryStat{}, PathOperationError(child.Resolved.DisplayPath, err)
	}
	return st, nil
}

// Stat reads entry metadata through the held root without opening content.
// Without follow it reports the final entry itself, including a dangling or
// outward-pointing link; follow resolves contained links only.
func (p BoundPath) Stat(follow bool) (fseffect.EntryStat, error) {
	if p.Null {
		return fseffect.EntryStat{}, invalidOperand(p.Resolved.DisplayPath, "null device is not a directory entry")
	}
	root, err := p.openRoot()
	if err != nil {
		return fseffect.EntryStat{}, PathOperationError(p.Resolved.DisplayPath, err)
	}
	defer root.Close()
	admit := p.admitRead
	if !follow {
		admit = p.admitEntry
	}
	st, err := root.Stat(p.exactRelative, follow, admit)
	if err != nil {
		return fseffect.EntryStat{}, PathOperationError(p.Resolved.DisplayPath, err)
	}
	return st, nil
}

// ReadDir lists a directory's entry names in unsigned byte order. The
// listing is the directory itself: no ignore rules, catalog or map apply.
func (p BoundPath) ReadDir() ([]string, error) {
	dir, err := p.OpenDir()
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.Names(), nil
}

// Child binds one entry beneath a bound directory with the same authority.
func (p BoundPath) Child(name string) BoundPath {
	child := p
	if p.exactRelative == "." || p.exactRelative == "" {
		child.exactRelative = name
	} else {
		child.exactRelative = p.exactRelative + string(filepath.Separator) + name
	}
	child.Resolved.Abs = p.Resolved.Abs + string(filepath.Separator) + name
	child.Resolved.DisplayPath = filepath.ToSlash(filepath.Join(p.Resolved.DisplayPath, name))
	if p.Resolved.ScopeRel == "" || p.Resolved.ScopeRel == "." {
		child.Resolved.ScopeRel = name
	} else {
		child.Resolved.ScopeRel = p.Resolved.ScopeRel + "/" + name
	}
	return child
}

// Readlink reads a link entry's target text, admitted at the entry itself, without
// authorizing or following the target.
func (p BoundPath) Readlink() (string, error) {
	if p.Null {
		return "", invalidOperand(p.Resolved.DisplayPath, "null device is not a directory entry")
	}
	root, err := p.openRoot()
	if err != nil {
		return "", PathOperationError(p.Resolved.DisplayPath, err)
	}
	defer root.Close()
	target, err := root.Readlink(p.exactRelative, p.admitEntry)
	return target, PathOperationError(p.Resolved.DisplayPath, err)
}

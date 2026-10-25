package projectpaths

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// Stat reads entry metadata through the held root without opening content.
// Without follow it reports the final entry itself, including a dangling or
// outward-pointing link; follow resolves contained links only.
func (p BoundPath) Stat(follow bool) (fseffect.EntryStat, error) {
	root, err := p.openEntryRoot()
	if err != nil {
		return fseffect.EntryStat{}, PathOperationError(p.Resolved.DisplayPath, err)
	}
	defer func() { _ = root.Close() }()
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
	file, err := p.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	if !info.IsDir() {
		return nil, PathOperationError(p.Resolved.DisplayPath, &fs.PathError{Op: "readdir", Path: p.Resolved.DisplayPath, Err: errors.New("not a directory")})
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	slices.Sort(names)
	return names, nil
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
	root, err := p.openEntryRoot()
	if err != nil {
		return "", PathOperationError(p.Resolved.DisplayPath, err)
	}
	defer func() { _ = root.Close() }()
	target, err := root.Readlink(p.exactRelative, p.admitEntry)
	return target, PathOperationError(p.Resolved.DisplayPath, err)
}

// openEntryRoot distinguishes namespace metadata from the null byte stream.
func (p BoundPath) openEntryRoot() (*fseffect.ReadRoot, error) {
	if p.Null {
		return nil, invalidOperand(p.Resolved.DisplayPath, "null device is not a directory entry")
	}
	return p.openRoot()
}

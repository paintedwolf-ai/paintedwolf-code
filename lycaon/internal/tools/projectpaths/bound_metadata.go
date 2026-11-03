package projectpaths

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// BoundDir holds a bound directory and its root descriptor open while listing
// and inspecting its immediate entries.
type BoundDir struct {
	dir           BoundPath
	root          *fseffect.ReadRoot
	ownsRoot      bool
	file          *os.File
	names         []string
	listed        bool
	includeHidden bool
	closed        bool
	listErr       error
}

// OpenDir admits and holds a directory without reading its membership.
func (p BoundPath) OpenDir() (*BoundDir, error) {
	return p.OpenDirWithRoot(nil)
}

// OpenDirWithRoot borrows shared when supplied; Close releases only owned roots.
func (p BoundPath) OpenDirWithRoot(shared *fseffect.ReadRoot) (*BoundDir, error) {
	if p.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Cause: errors.New("null input is an empty stream, not a file")}
	}
	root := shared
	ownsRoot := false
	if root == nil {
		var err error
		root, err = p.openRoot()
		if err != nil {
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
		ownsRoot = true
	}
	closeRoot := func() {
		if ownsRoot {
			_ = root.Close()
		}
	}
	file, err := root.OpenExact(p.exactRelative)
	if err != nil {
		closeRoot()
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	return bindOpenDirectory(p, root, ownsRoot, file)
}

// DirectoryEntriesLimitError reports membership beyond a consumer's bound.
type DirectoryEntriesLimitError struct{ Entries int }

func (e *DirectoryEntriesLimitError) Error() string {
	return "directory membership exceeds entry limit"
}

// DirectoryNames selects visible membership within a retained-name bound.
type DirectoryNames struct {
	Maximum       int
	IncludeHidden bool
}

// Names reads byte-ordered membership with one hidden-entry selection.
func (d *BoundDir) Names(options DirectoryNames) ([]string, error) {
	if d.listed {
		if err := d.AdmitDirectoryRead(); err != nil {
			return nil, err
		}
		if d.includeHidden != options.IncludeHidden {
			return nil, errors.New("directory membership selection changed")
		}
		if d.listErr == nil && options.Maximum > 0 && len(d.names) > options.Maximum {
			return nil, &DirectoryEntriesLimitError{Entries: options.Maximum + 1}
		}
		return d.names, d.listErr
	}
	d.listed, d.includeHidden = true, options.IncludeHidden
	var names []string
	for {
		if err := d.AdmitDirectoryRead(); err != nil {
			d.listErr = err
			return nil, err
		}
		batch, err := d.file.Readdirnames(1024)
		for _, name := range batch {
			if d.VisibleChild(name) && (options.IncludeHidden || !strings.HasPrefix(name, ".")) {
				names = append(names, name)
				if options.Maximum > 0 && len(names) > options.Maximum {
					d.listErr = &DirectoryEntriesLimitError{Entries: options.Maximum + 1}
					return nil, d.listErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			d.listErr = PathOperationError(d.dir.Resolved.DisplayPath, err)
			return nil, d.listErr
		}
	}
	slices.Sort(names)
	d.names = names
	return d.names, nil
}

// Child admits an immediate name without reading its metadata or link target.
func (d *BoundDir) Child(name string) (BoundPath, error) {
	if d.closed {
		return BoundPath{}, os.ErrClosed
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, 0) {
		return BoundPath{}, invalidOperand(name, "expected an immediate directory entry")
	}
	child := d.dir.Child(name)
	if !d.VisibleChild(name) {
		return child, PathOperationError(child.Resolved.DisplayPath, fs.ErrNotExist)
	}
	if child.admitEntry != nil {
		dirName, err := d.currentPath()
		if err != nil {
			return child, PathOperationError(child.Resolved.DisplayPath, err)
		}
		actual := dirName + string(filepath.Separator) + name
		if strings.HasSuffix(dirName, string(filepath.Separator)) {
			actual = filepath.Join(dirName, name)
		}
		if err := child.admitEntry(actual); err != nil {
			return child, PathOperationError(child.Resolved.DisplayPath, err)
		}
	}
	return child, nil
}

// VisibleChild applies the namespace's recovery exclusions.
func (d *BoundDir) VisibleChild(name string) bool {
	if strings.Contains(d.dir.Resolved.Abs, settingsoverlay.DirName()) || name == settingsoverlay.DirName() || name == settingsoverlay.RecoveryDirName {
		child := d.dir.Resolved.Abs + string(filepath.Separator) + name
		return !settingsoverlay.HasRecoverySegment(child) && !settingsoverlay.IsRecoveryOnlyDir(child)
	}
	return true
}

// Close releases the held directory and root descriptors.
func (d *BoundDir) Close() error {
	d.closed = true
	if !d.ownsRoot {
		return d.file.Close()
	}
	return errors.Join(d.file.Close(), d.root.Close())
}

// StatChild binds and stats one immediate entry of the held directory without following links.
func (d *BoundDir) StatChild(name string) (BoundPath, fseffect.EntryStat, error) {
	if d.closed {
		return BoundPath{}, fseffect.EntryStat{}, os.ErrClosed
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, 0) {
		return BoundPath{}, fseffect.EntryStat{}, invalidOperand(name, "expected an immediate directory entry")
	}
	child := d.dir.Child(name)
	if !d.VisibleChild(name) {
		return child, fseffect.EntryStat{}, PathOperationError(child.Resolved.DisplayPath, fs.ErrNotExist)
	}
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
	if settingsoverlay.IsRecoveryRelPath(p.Resolved.ScopeRel) || settingsoverlay.HasRecoverySegment(p.Resolved.Abs) || settingsoverlay.IsRecoveryOnlyDir(p.Resolved.Abs) {
		return fseffect.EntryStat{}, PathOperationError(p.Resolved.DisplayPath, &fs.PathError{Op: "stat", Path: p.Resolved.DisplayPath, Err: fs.ErrNotExist})
	}
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
	dir, err := p.OpenDir()
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return dir.Names(DirectoryNames{IncludeHidden: true})
}

// Child binds one entry beneath a bound directory with the same authority.
func (p BoundPath) Child(name string) BoundPath {
	child := p
	if p.Resolved.ScopeRel == "" || p.Resolved.ScopeRel == "." {
		child.Resolved.ScopeRel = name
	} else {
		child.Resolved.ScopeRel = p.Resolved.ScopeRel + "/" + name
	}
	if filepath.Separator == '/' && p.exactRelative == p.Resolved.ScopeRel {
		child.exactRelative = child.Resolved.ScopeRel
	} else if p.exactRelative == "." || p.exactRelative == "" {
		child.exactRelative = name
	} else {
		child.exactRelative = p.exactRelative + string(filepath.Separator) + name
	}
	child.Resolved.Abs = p.Resolved.Abs + string(filepath.Separator) + name
	if p.Resolved.DisplayPath == p.Resolved.ScopeRel && name != "" && name != "." && name != ".." && filepath.Base(name) == name {
		child.Resolved.DisplayPath = child.Resolved.ScopeRel
	} else {
		child.Resolved.DisplayPath = filepath.ToSlash(filepath.Join(p.Resolved.DisplayPath, name))
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

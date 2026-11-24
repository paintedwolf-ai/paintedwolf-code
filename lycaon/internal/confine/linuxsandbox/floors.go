package linuxsandbox

import (
	"path/filepath"
	"strings"
)

// NamedFloor protects every entry named Name below the directory Below, at any
// depth: a loader file such as AGENTS.md.
type NamedFloor struct {
	Below string `json:"below"`
	Name  string `json:"name"`
}

// creationFloors decides the name-creating calls the supervisor performs. A
// mount protects only an entry that exists at launch, and Landlock cannot
// refuse one name below a granted directory, so a protected location that does
// not exist yet is held here by name. Names and paths fold case, as the
// loaders do, so a case-insensitive directory cannot alias one.
type creationFloors struct {
	sharedTemp     []string
	readRoots      []string
	readFiles      []string
	writeOnlyRoots []string
	writeOnlyFiles []string
	hidden         []string
	// protected are write- and read-floor locations, existing or not.
	protected []string
	// names are basenames protected at any depth below a directory.
	names []NamedFloor
	// grants are approved writes beneath a protected location.
	grants []string
	// sensitive holds, folded, every component name of a protected location
	// and every protected basename.
	sensitive map[string]bool
}

func newCreationFloors(fs Filesystem) creationFloors {
	f := creationFloors{
		sharedTemp:     fs.SharedTemp,
		readRoots:      fs.ReadRoots,
		readFiles:      fs.ReadFiles,
		writeOnlyRoots: fs.WriteOnlyRoots,
		writeOnlyFiles: fs.WriteOnlyFiles,
		hidden:         fs.Hidden,
		names:          fs.ReadOnlyNames,
		sensitive:      map[string]bool{},
	}
	f.protected = append(append(f.protected, fs.ReadOnly...), fs.Hidden...)
	f.grants = append(append(f.grants, fs.WriteFiles...), fs.WriteRoots...)
	for _, p := range f.protected {
		for _, component := range strings.Split(filepath.Clean(p), "/") {
			if component != "" {
				f.sensitive[strings.ToLower(component)] = true
			}
		}
	}
	for _, n := range f.names {
		f.sensitive[strings.ToLower(n.Name)] = true
	}
	return f
}

// mayRefuseCreate reports whether creating an entry named base could be
// refused. An entry is refused only where it would be a protected location, a
// protected name, or below a protected location. The first two end in a
// sensitive name. The last needs that location to exist as a directory: one
// that existed at launch is covered by a read-only or empty mount, which the
// kernel refuses to create in except where a grant restores a subtree, which
// the floors admit too; and one that did not can come to exist only by
// creating, linking or moving an entry with its name, which the floors decide.
// So any other name is created as the caller asked.
func (f creationFloors) mayRefuseCreate(base string) bool {
	return f.sensitive[strings.ToLower(base)]
}

// creation is one name a call would create, link, or move.
type creation int

const (
	// createEntry makes a new entry: a file, directory, node, symlink, or link.
	createEntry creation = iota
	// createDirectory makes a new directory, which may lead toward a grant.
	createDirectory
	// renameSource moves an existing entry away from its path.
	renameSource
	// renameTarget moves an entry, with everything beneath it, to its path.
	renameTarget
)

// refuses reports whether the floors hold path against the creation.
func (f creationFloors) refuses(kind creation, path string) bool {
	path = filepath.Clean(strings.TrimSuffix(path, " (deleted)"))
	for _, p := range f.protected {
		if !atOrUnderFold(path, p) {
			continue
		}
		if f.granted(path, p) || (kind == createDirectory && f.leadsToGrant(path, p)) {
			continue
		}
		return true
	}
	for _, n := range f.names {
		if path != n.Below && atOrUnderFold(path, n.Below) && strings.EqualFold(filepath.Base(path), n.Name) && !f.exactGrant(path) {
			return true
		}
	}
	if kind != renameTarget {
		return false
	}
	// A directory moved onto an ancestor of a protected location would place
	// whatever it holds there, unreviewed.
	for _, p := range f.protected {
		if atOrUnderFold(p, path) {
			return true
		}
	}
	return false
}

// crossesNamedFloor reports whether moving oldPath to newPath crosses the
// boundary of any NamedFloor.
func (f creationFloors) crossesNamedFloor(oldPath, newPath string) bool {
	for _, n := range f.names {
		if atOrUnderFold(oldPath, n.Below) != atOrUnderFold(newPath, n.Below) {
			return true
		}
	}
	return false
}

// isNamedFloorBase reports whether name matches any NamedFloor basename.
func (f creationFloors) isNamedFloorBase(name string) bool {
	for _, n := range f.names {
		if strings.EqualFold(name, n.Name) {
			return true
		}
	}
	return false
}

// granted reports an approved write at or above path inside protected p.
func (f creationFloors) granted(path, p string) bool {
	for _, g := range f.grants {
		if atOrUnderFold(g, p) && atOrUnderFold(path, g) {
			return true
		}
	}
	return false
}

// leadsToGrant reports a directory on the way to a grant inside protected p.
// Everything else created in it stays held.
func (f creationFloors) leadsToGrant(path, p string) bool {
	for _, g := range f.grants {
		if atOrUnderFold(g, p) && atOrUnderFold(g, path) {
			return true
		}
	}
	return false
}

func (f creationFloors) exactGrant(path string) bool {
	for _, g := range f.grants {
		if strings.EqualFold(filepath.Clean(g), path) {
			return true
		}
	}
	return false
}

// refusesTempRead reports whether shared-temp policy denies reading or listing path.
func (f creationFloors) refusesTempRead(path string, asDir bool) bool {
	path = filepath.Clean(strings.TrimSuffix(path, " (deleted)"))
	tempRoot := f.enclosingSharedTemp(path)
	if tempRoot == "" {
		return false
	}
	if f.hiddenBelowTemp(path, tempRoot) {
		return true
	}
	if !asDir {
		for _, file := range f.writeOnlyFiles {
			if strings.EqualFold(filepath.Clean(file), path) {
				return true
			}
		}
		for _, file := range f.readFiles {
			if strings.EqualFold(filepath.Clean(file), path) {
				return false
			}
		}
	}
	bestRead, bestWriteOnly := -1, -1
	for _, r := range f.readRoots {
		if atOrUnderFold(path, r) && len(r) > bestRead {
			bestRead = len(r)
		}
	}
	for _, w := range f.writeOnlyRoots {
		if atOrUnderFold(path, w) && len(w) > bestWriteOnly {
			bestWriteOnly = len(w)
		}
	}
	return bestRead <= bestWriteOnly
}

func (f creationFloors) enclosingSharedTemp(path string) string {
	var best string
	for _, loc := range f.sharedTemp {
		if atOrUnderFold(path, loc) && len(loc) > len(best) {
			best = loc
		}
	}
	return best
}

func (f creationFloors) hiddenBelowTemp(path, tempRoot string) bool {
	bestHidden := -1
	for _, p := range f.hidden {
		if len(p) > len(tempRoot) && atOrUnderFold(p, tempRoot) && atOrUnderFold(path, p) && len(p) > bestHidden {
			bestHidden = len(p)
		}
	}
	if bestHidden < 0 {
		return false
	}
	for _, r := range f.readRoots {
		if atOrUnderFold(path, r) && len(r) >= bestHidden {
			return false
		}
	}
	for _, file := range f.readFiles {
		if strings.EqualFold(filepath.Clean(file), path) && len(file) >= bestHidden {
			return false
		}
	}
	return true
}

// atOrUnderFold reports whether path is root or below it, comparing each
// component without case.
func atOrUnderFold(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	if len(path) < len(root) || !strings.EqualFold(path[:len(root)], root) {
		return false
	}
	return len(path) == len(root) || root == "/" || path[len(root)] == '/'
}

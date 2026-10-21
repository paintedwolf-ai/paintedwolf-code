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
	// protected are write- and read-floor locations, existing or not.
	protected []string
	// names are basenames protected at any depth below a directory.
	names []NamedFloor
	// grants are approved writes beneath a protected location.
	grants []string
}

func newCreationFloors(fs Filesystem) creationFloors {
	f := creationFloors{names: fs.ReadOnlyNames}
	f.protected = append(append(f.protected, fs.ReadOnly...), fs.Hidden...)
	f.grants = append(append(append(f.grants, fs.Restored...), fs.WriteFiles...), fs.WriteRoots...)
	return f
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
	path = filepath.Clean(path)
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

// atOrUnderFold reports whether path is root or below it, comparing each
// component without case.
func atOrUnderFold(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	if len(path) < len(root) || !strings.EqualFold(path[:len(root)], root) {
		return false
	}
	return len(path) == len(root) || root == "/" || path[len(root)] == '/'
}

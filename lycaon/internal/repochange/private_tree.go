package repochange

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

var privateTrees = struct {
	sync.RWMutex
	paths map[string]int
}{paths: make(map[string]int)}

// HoldPrivateTree excludes an operation's staging tree from source observation.
func HoldPrivateTree(path string) func() {
	path = fspath.CanonicalPath(path)
	if path == "" {
		return func() {}
	}
	privateTrees.Lock()
	privateTrees.paths[path]++
	privateTrees.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			// Native watcher batches can arrive after publication and cleanup.
			time.AfterFunc(2*time.Second, func() {
				privateTrees.Lock()
				privateTrees.paths[path]--
				if privateTrees.paths[path] == 0 {
					delete(privateTrees.paths, path)
				}
				privateTrees.Unlock()
			})
		})
	}
}

// IsPrivatePath reports whether path lies in a held private tree, is excluded
// by the host control-plane observation boundary, or is an entry the
// durable-write door is staging.
func IsPrivatePath(path string) bool {
	return IsPrivatePathForRoot("", path)
}

// IsPrivatePathForRoot reports whether path is private or excluded from
// observation when observed under rootPath.
func IsPrivatePathForRoot(rootPath, path string) bool {
	if fseffect.IsStaging(filepath.Dir(path), filepath.Base(path)) {
		return true
	}
	if settingsoverlay.HasRecoverySegment(path) || settingsoverlay.IsRecoveryOnlyDir(path) {
		return true
	}
	if enginepaths.IsObservedStateExcluded(rootPath, path) {
		return true
	}
	privateTrees.RLock()
	defer privateTrees.RUnlock()
	if len(privateTrees.paths) == 0 {
		return false
	}
	path = filepath.ToSlash(filepath.Join(fspath.CanonicalPath(filepath.Dir(path)), filepath.Base(path)))
	for root := range privateTrees.paths {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// PrivateDirectoryFilter lazily canonicalizes a listed directory once while
// checking every child against the current private-tree registrations and
// control-plane observation boundaries.
type PrivateDirectoryFilter struct {
	root       string
	directory  string
	dirOnce    sync.Once
	canonDir   string
	excludeAll bool
	once       sync.Once
	canonical  string
}

// NewPrivateDirectoryFilter prepares a directory whose direct entries will be
// checked repeatedly. Resolution is deferred until needed.
func NewPrivateDirectoryFilter(directory string) *PrivateDirectoryFilter {
	return &PrivateDirectoryFilter{directory: directory}
}

// NewPrivateDirectoryFilterForRoot prepares a directory filter scoped to an
// attached rootPath.
func NewPrivateDirectoryFilterForRoot(rootPath, directory string) *PrivateDirectoryFilter {
	return &PrivateDirectoryFilter{root: rootPath, directory: directory}
}

// Contains reports whether the named direct child is currently private.
func (f *PrivateDirectoryFilter) Contains(name string) bool {
	return f.ContainsEntry(name, false)
}

// ContainsEntry reports whether the named direct child is currently private,
// resolving symlink targets when isSymlink is true.
func (f *PrivateDirectoryFilter) ContainsEntry(name string, isSymlink bool) bool {
	if fseffect.IsStaging(f.directory, name) {
		return true
	}
	candidate := filepath.Join(f.directory, name)
	if settingsoverlay.HasRecoverySegment(candidate) || settingsoverlay.IsRecoveryOnlyDir(candidate) {
		return true
	}
	if f.root != "" {
		f.dirOnce.Do(func() {
			if enginepaths.IsObservedStateExcluded(f.root, f.directory) {
				f.excludeAll = true
				return
			}
			if c := fspath.CanonicalPath(f.directory); c != "" {
				f.canonDir = filepath.FromSlash(c)
			}
		})
		if f.excludeAll {
			return true
		}
	}
	if enginepaths.IsObservedDirChildExcluded(f.root, f.directory, name, false) {
		return true
	}
	if f.canonDir != "" && f.canonDir != f.directory && enginepaths.IsObservedDirChildExcluded(f.root, f.canonDir, name, false) {
		return true
	}
	if isSymlink && enginepaths.IsObservedStateExcluded(f.root, candidate) {
		return true
	}
	privateTrees.RLock()
	havePrivateTrees := len(privateTrees.paths) != 0
	privateTrees.RUnlock()
	if !havePrivateTrees {
		return false
	}
	f.once.Do(func() { f.canonical = fspath.CanonicalPath(f.directory) })
	privateTrees.RLock()
	defer privateTrees.RUnlock()
	if len(privateTrees.paths) == 0 {
		return false
	}
	if f.canonical == "" {
		return true
	}
	path := filepath.ToSlash(filepath.Join(f.canonical, name))
	for root := range privateTrees.paths {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

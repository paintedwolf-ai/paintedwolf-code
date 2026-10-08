package repochange

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
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

// IsPrivatePath reports whether path lies in a held private tree or is an
// entry the durable-write door is staging.
func IsPrivatePath(path string) bool {
	if fseffect.IsStaging(filepath.Dir(path), filepath.Base(path)) {
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
// checking every child against the current private-tree registrations.
type PrivateDirectoryFilter struct {
	directory string
	once      sync.Once
	canonical string
}

// NewPrivateDirectoryFilter prepares a directory whose direct entries will be
// checked repeatedly. Resolution is deferred until a private tree exists.
func NewPrivateDirectoryFilter(directory string) *PrivateDirectoryFilter {
	return &PrivateDirectoryFilter{directory: directory}
}

// Contains reports whether the named direct child is currently private.
func (f *PrivateDirectoryFilter) Contains(name string) bool {
	if fseffect.IsStaging(f.directory, name) {
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

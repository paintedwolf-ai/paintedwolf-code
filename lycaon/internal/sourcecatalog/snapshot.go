package sourcecatalog

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

// State is the lifecycle of one catalog generation.
type State string

const (
	StateWarming State = "warming"
	StateReady   State = "ready"
	StateFailed  State = "failed"
)

// Root identifies one attached filesystem tree, or a subtree of one.
type Root struct {
	ID   string
	Path string
	// Within is the attached root a subtree generation belongs to; empty for an
	// attached root. Its watcher covers the subtree and its events reach it.
	Within string
}

// Scoped reports a subtree generation.
func (r Root) Scoped() bool { return r.Within != "" }

// watchRoot is the attached root whose watcher reports this tree's changes.
func (r Root) watchRoot() string {
	if r.Within != "" {
		return r.Within
	}
	return r.Path
}

// Entry is one cataloged path below a root.
type Entry struct {
	RootID      string
	Path        string
	Parent      string
	Name        string
	Depth       int
	IsDir       bool
	IsSymlink   bool
	TargetIsDir bool
	IsVCSRoot   bool
	Size        int64
	Mode        uint32
	Modified    time.Time
	// Boundary marks a directory the walk did not enter because a budget
	// fell there. Its listing is served live, one level at a time.
	Boundary bool
}

// Snapshot is an immutable source-tree generation.
type Snapshot struct {
	State      State
	Revision   uint64
	Refreshing bool
	Error      string
	Roots      []Root
	Entries    []Entry
	children   map[string][]int
	dirs       []int
	Epochs     map[string]repochange.Epoch
	// Moving means the walk completed, but a write overtook its epoch pin. The
	// generation is usable as an observation and remains stale for reconciliation.
	Moving bool
	// RootBoundary means the walk budget ran out before the roots were
	// fully observed; what the generation holds is a prefix of the tree.
	RootBoundary bool
	// boundaries lists each root's boundary directories, sorted, so a change
	// below one is recognized without a scan of the entries.
	boundaries map[string][]string
}

// underBoundary reports whether rel lies at or below a directory this
// generation did not enter.
func (s Snapshot) underBoundary(rootID, rel string) bool {
	for _, boundary := range s.boundaries[rootID] {
		if rel == boundary || strings.HasPrefix(rel, boundary+"/") {
			return true
		}
	}
	return false
}

// Listing returns the sorted immediate children of dir.
func (s Snapshot) Listing(rootID, dir string) ([]Entry, bool) {
	dir = normalizeDir(dir)
	indices, ok := s.children[entryKey(rootID, dir)]
	if !ok {
		return nil, false
	}
	out := make([]Entry, 0, len(indices))
	for _, index := range indices {
		out = append(out, s.Entries[index])
	}
	return out, true
}

// Directories returns directory entries breadth-first. The root listing is
// represented by one synthetic entry per root with Path=".".
func (s Snapshot) Directories(rootID, dir string) []Entry {
	dir = normalizeDir(dir)
	rootDepth := pathDepth(dir)
	out := make([]Entry, 0, len(s.dirs)+1)
	for _, root := range s.Roots {
		if rootID != "" && root.ID != rootID {
			continue
		}
		if dir == "." {
			out = append(out, Entry{RootID: root.ID, Path: ".", Name: ".", IsDir: true})
		} else if entry, ok := s.Entry(root.ID, dir); ok && entry.IsDir {
			out = append(out, entry)
		}
	}
	for _, index := range s.dirs {
		entry := s.Entries[index]
		if rootID != "" && entry.RootID != rootID {
			continue
		}
		if entry.Path == dir || !underDir(entry.Path, dir) {
			continue
		}
		if dir != "." && entry.Depth <= rootDepth {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Entry returns one exact path.
func (s Snapshot) Entry(rootID, rel string) (Entry, bool) {
	rel = normalizeDir(rel)
	if rel == "." {
		for _, root := range s.Roots {
			if root.ID == rootID {
				return Entry{RootID: rootID, Path: ".", Name: ".", IsDir: true}, true
			}
		}
		return Entry{}, false
	}
	parent := normalizeDir(path.Dir(rel))
	for _, index := range s.children[entryKey(rootID, parent)] {
		entry := s.Entries[index]
		if entry.Path == rel {
			return entry, true
		}
	}
	return Entry{}, false
}

// Snapshot returns a ready generation, joining an in-flight build when needed.
func (c *Catalog) Snapshot(ctx context.Context, projectID string, roots []Root) (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("source catalog is nil")
	}
	cleaned, err := cleanRoots(roots)
	if err != nil {
		return Snapshot{}, err
	}
	type result struct {
		snapshot Snapshot
		err      error
	}
	results := make([]result, len(cleaned))
	var wg sync.WaitGroup
	for i, root := range cleaned {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].snapshot, results[i].err = c.snapshotRoot(ctx, projectID, root)
		}()
	}
	wg.Wait()
	parts := make([]Snapshot, 0, len(results))
	for _, result := range results {
		if result.err != nil {
			return combineSnapshots(cleaned, parts), result.err
		}
		parts = append(parts, result.snapshot)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return combineSnapshots(cleaned, parts), nil
}

// observedChanges drops the changed paths that fall below a boundary of the
// generation. Paths the generation cannot place are kept.
func (s Snapshot) observedChanges(root Root, paths []string) []string {
	if len(s.boundaries[root.ID]) == 0 {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, changed := range paths {
		rel := changed
		if filepath.IsAbs(changed) {
			relative, err := filepath.Rel(root.Path, changed)
			if err != nil {
				kept = append(kept, changed)
				continue
			}
			rel = relative
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if s.underBoundary(root.ID, rel) && !s.isBoundary(root.ID, rel) {
			continue
		}
		kept = append(kept, changed)
	}
	return kept
}

// A boundary directory remains observed even though its descendants are not.
func (s Snapshot) isBoundary(rootID, rel string) bool {
	for _, boundary := range s.boundaries[rootID] {
		if rel == boundary {
			return true
		}
	}
	return false
}

func (s *Snapshot) sort() {
	for _, indices := range s.children {
		sort.SliceStable(indices, func(i, j int) bool {
			left, right := s.Entries[indices[i]], s.Entries[indices[j]]
			if left.IsDir != right.IsDir {
				return left.IsDir
			}
			li, lj := strings.ToLower(left.Name), strings.ToLower(right.Name)
			if li != lj {
				return li < lj
			}
			return left.Name < right.Name
		})
	}
	sort.SliceStable(s.dirs, func(i, j int) bool {
		left, right := s.Entries[s.dirs[i]], s.Entries[s.dirs[j]]
		if left.Depth != right.Depth {
			return left.Depth < right.Depth
		}
		if left.RootID != right.RootID {
			return left.RootID < right.RootID
		}
		return strings.ToLower(left.Path) < strings.ToLower(right.Path)
	})
	s.rebuildBoundaryIndex()
}

func (s *Snapshot) rebuildBoundaryIndex() {
	s.boundaries = nil
	for _, index := range s.dirs {
		entry := s.Entries[index]
		if entry.Boundary {
			if s.boundaries == nil {
				s.boundaries = make(map[string][]string)
			}
			s.boundaries[entry.RootID] = append(s.boundaries[entry.RootID], entry.Path)
		}
	}
}

// pinnedAt returns the generation vouched for at epoch. Only the pin changes,
// so the generation keeps its revision.
func (s Snapshot) pinnedAt(rootID string, epoch repochange.Epoch) Snapshot {
	s.Epochs = map[string]repochange.Epoch{rootID: epoch}
	return s
}

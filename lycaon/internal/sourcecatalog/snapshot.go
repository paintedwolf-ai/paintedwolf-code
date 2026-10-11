package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
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

// buildSnapshot records budget-limited directories as boundaries for live listing.
func buildSnapshot(ctx context.Context, roots []Root, policy walkPolicy) (Snapshot, error) {
	snapshot := Snapshot{State: StateReady, Roots: append([]Root(nil), roots...), Entries: make([]Entry, 0, 1024)}
	for _, root := range roots {
		boundaries := make(map[string]struct{})
		opts := policy.options()
		if len(roots) > 1 {
			// A policy orders one root; several roots share only its budgets.
			opts.Scope = nil
		}
		opts.OnBoundary = func(b sandbox.SurveyBoundary) {
			boundaries[b.Rel] = struct{}{}
			slog.DebugContext(ctx, "catalog walk boundary", "root", root.Path, "path", b.Rel, "reason", string(b.Reason), "entries", b.Entries)
		}
		err := sandbox.SurveyWalk(ctx, root.Path, opts, func(item sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if repochange.IsPrivatePath(item.Abs) {
				return sandbox.SurveySkipDir, nil
			}
			if err := nextMetadataEntry(ctx); err != nil {
				return sandbox.SurveyStop, err
			}
			info, infoErr := item.DirEntry.Info()
			if infoErr != nil {
				return sandbox.SurveyContinue, fmt.Errorf("catalog metadata %s: %w", item.Rel, infoErr)
			}
			rel := filepath.ToSlash(item.Rel)
			parent := normalizeDir(path.Dir(rel))
			entry := Entry{
				RootID: root.ID, Path: rel, Parent: parent, Name: path.Base(rel), Depth: item.Depth,
				IsDir: item.IsDir, IsSymlink: item.IsSymlink, Size: info.Size(),
				Mode: uint32(info.Mode()), Modified: info.ModTime().UTC(),
			}
			if item.IsDir {
				entry.IsVCSRoot = gitrepo.IsRoot(item.Abs)
			}
			if item.IsSymlink {
				if target, statErr := os.Stat(item.Abs); statErr == nil {
					entry.TargetIsDir = target.IsDir()
				}
			}
			snapshot.Entries = append(snapshot.Entries, entry)
			return sandbox.SurveyContinue, nil
		})
		if err != nil {
			return Snapshot{Roots: append([]Root(nil), roots...)}, fmt.Errorf("catalog root %s: %w", root.ID, err)
		}
		if len(boundaries) > 0 {
			for i := range snapshot.Entries {
				entry := &snapshot.Entries[i]
				if entry.RootID != root.ID || !entry.IsDir {
					continue
				}
				if _, bounded := boundaries[entry.Path]; bounded {
					entry.Boundary = true
				}
			}
			if _, rootBounded := boundaries["."]; rootBounded {
				snapshot.RootBoundary = true
			}
		}
	}
	snapshot.index()
	return snapshot, nil
}

func cleanRoots(roots []Root) ([]Root, error) {
	if len(roots) == 0 {
		return nil, errors.New("source catalog requires at least one root")
	}
	cleaned := make([]Root, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		id := strings.TrimSpace(root.ID)
		rootPath := cleanAbs(root.Path)
		if id == "" || rootPath == "" {
			return nil, errors.New("source catalog root id and path are required")
		}
		key := id + "\x00" + rootPath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, Root{ID: id, Path: rootPath, Within: cleanAbs(root.Within)})
	}
	return cleaned, nil
}

func rootKey(projectID string, root Root) string {
	return strings.TrimSpace(projectID) + "\x00" + root.ID + "\x00" + root.Path
}

func combineSnapshots(roots []Root, parts []Snapshot) Snapshot {
	out := Snapshot{
		State: StateReady, Roots: append([]Root(nil), roots...),
		Entries: make([]Entry, 0), Epochs: make(map[string]repochange.Epoch),
	}
	for _, part := range parts {
		for rootID, epoch := range part.Epochs {
			out.Epochs[rootID] = epoch
		}
		if part.Revision > out.Revision {
			out.Revision = part.Revision
		}
		out.Refreshing = out.Refreshing || part.Refreshing
		out.Moving = out.Moving || part.Moving
		if part.State == StateFailed {
			out.State = StateFailed
		} else if part.State == StateWarming && out.State == StateReady {
			out.State = StateWarming
		}
		if part.Error != "" {
			if out.Error != "" {
				out.Error += "; "
			}
			out.Error += part.Error
		}
		out.Entries = append(out.Entries, part.Entries...)
	}
	out.index()
	return out
}

func entryKey(rootID, dir string) string { return rootID + "\x00" + normalizeDir(dir) }

func normalizeDir(dir string) string {
	dir = strings.TrimSpace(filepath.ToSlash(dir))
	if dir == "" || dir == "." {
		return "."
	}
	return strings.TrimPrefix(path.Clean("/"+dir), "/")
}

func underDir(candidate, dir string) bool {
	dir = normalizeDir(dir)
	return dir == "." || strings.HasPrefix(candidate, dir+"/")
}

func pathDepth(rel string) int {
	rel = normalizeDir(rel)
	if rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func cleanAbs(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// WalkStep tells Walk how to continue after one entry.
type WalkStep uint8

const (
	WalkContinue WalkStep = iota
	// WalkSkip leaves the visited directory's descendants unvisited.
	WalkSkip
	WalkStop
)

// NewSnapshot indexes entries as one ready generation over roots.
func NewSnapshot(roots []Root, entries []Entry) Snapshot {
	s := Snapshot{State: StateReady, Roots: append([]Root(nil), roots...), Entries: entries}
	s.index()
	return s
}

// index derives the children, directory, and boundary indexes from Entries.
func (s *Snapshot) index() {
	s.children = make(map[string][]int, len(s.Roots)+len(s.Entries)/8)
	s.dirs = nil
	for _, root := range s.Roots {
		s.children[entryKey(root.ID, ".")] = []int{}
	}
	for i, entry := range s.Entries {
		parent := entryKey(entry.RootID, entry.Parent)
		s.children[parent] = append(s.children[parent], i)
		if entry.IsDir || entry.TargetIsDir {
			if key := entryKey(entry.RootID, entry.Path); s.children[key] == nil {
				s.children[key] = []int{}
			}
			s.dirs = append(s.dirs, i)
		}
	}
	s.sort()
}

// Walk visits the entries below dir depth-first, each directory's children in
// listing order. A skipped directory costs one visit whatever its subtree
// holds, so a walk costs what it admits rather than what the root holds.
// Symlinked directories are visited but not entered.
func (s Snapshot) Walk(ctx context.Context, rootID, dir string, visit func(Entry) WalkStep) error {
	pending := [][]int{s.children[entryKey(rootID, dir)]}
	for len(pending) > 0 {
		top := len(pending) - 1
		if len(pending[top]) == 0 {
			pending = pending[:top]
			continue
		}
		index := pending[top][0]
		pending[top] = pending[top][1:]
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := s.Entries[index]
		switch visit(entry) {
		case WalkStop:
			return nil
		case WalkSkip:
			continue
		case WalkContinue:
		}
		if entry.IsDir && !entry.IsSymlink {
			if children := s.children[entryKey(rootID, entry.Path)]; len(children) > 0 {
				pending = append(pending, children)
			}
		}
	}
	return nil
}

package sourcecatalog

import "context"

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

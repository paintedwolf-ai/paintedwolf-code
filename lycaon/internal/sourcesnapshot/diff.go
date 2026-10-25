package sourcesnapshot

import "context"

// Diff reports every path that differs between two generations, reading
// only the chunks their manifests do not share. An empty previous id makes
// every entry of current an addition.
func (s *Store) Diff(ctx context.Context, previousID, currentID string, fn func(Change) error) error {
	var previous *snapshotView
	if previousID != "" {
		view, err := s.open(ctx, previousID)
		if err != nil {
			return err
		}
		defer view.release()
		previous = view
	}
	current, err := s.open(ctx, currentID)
	if err != nil {
		return err
	}
	defer current.release()
	for _, root := range diffRoots(previous, current) {
		rootPath := root.Path
		var before, after string
		if previous != nil {
			before = previous.manifests[rootPath]
		}
		after = current.manifests[rootPath]
		if before == after {
			continue
		}
		if err := s.diffManifests(ctx, rootPath, before, after, fn); err != nil {
			return err
		}
	}
	return nil
}

// diffRoots lists every root either generation holds, in path order.
func diffRoots(previous, current *snapshotView) []Root {
	var roots []Root
	if previous != nil {
		roots = append(roots, previous.Roots...)
	}
	return normalizeRoots(append(roots, current.Roots...))
}

// manifestCursor walks one manifest's entries a chunk at a time and can
// skip a chunk whole when it starts exactly where the cursor stands.
type manifestCursor struct {
	store    *Store
	rootPath string
	layout   []manifestChunk
	next     int
	entries  []Entry
}

func (s *Store) newManifestCursor(ctx context.Context, manifest, rootPath string) (*manifestCursor, error) {
	c := &manifestCursor{store: s, rootPath: rootPath}
	if manifest == "" {
		return c, nil
	}
	layout, err := s.manifestLayout(ctx, manifest)
	if err != nil {
		return nil, err
	}
	c.layout = layout
	return c, nil
}

// atCut reports whether the cursor sits at the start of an unread chunk.
func (c *manifestCursor) atCut() bool { return len(c.entries) == 0 && c.next < len(c.layout) }

func (c *manifestCursor) nextChunkID() string { return c.layout[c.next].id }

func (c *manifestCursor) skipChunk() { c.next++ }

// head returns the next entry, loading a chunk when the current one is spent.
func (c *manifestCursor) head(ctx context.Context) (*Entry, error) {
	for len(c.entries) == 0 {
		if c.next >= len(c.layout) {
			return nil, nil
		}
		entries, err := c.store.chunkEntries(ctx, c.layout[c.next].id, c.rootPath)
		if err != nil {
			return nil, err
		}
		c.next++
		c.entries = entries
	}
	return &c.entries[0], nil
}

func (c *manifestCursor) advance() { c.entries = c.entries[1:] }

func (s *Store) diffManifests(ctx context.Context, rootPath, before, after string, fn func(Change) error) error {
	left, err := s.newManifestCursor(ctx, before, rootPath)
	if err != nil {
		return err
	}
	right, err := s.newManifestCursor(ctx, after, rootPath)
	if err != nil {
		return err
	}
	for {
		if left.atCut() && right.atCut() && left.nextChunkID() == right.nextChunkID() {
			left.skipChunk()
			right.skipChunk()
			continue
		}
		l, err := left.head(ctx)
		if err != nil {
			return err
		}
		r, err := right.head(ctx)
		if err != nil {
			return err
		}
		switch {
		case l == nil && r == nil:
			return nil
		case r == nil || (l != nil && l.Path < r.Path):
			removed := *l
			left.advance()
			err = fn(Change{Before: &removed})
		case l == nil || r.Path < l.Path:
			added := *r
			right.advance()
			err = fn(Change{After: &added})
		default:
			was, now := *l, *r
			left.advance()
			right.advance()
			if !was.SameContent(now) || was.Mode != now.Mode {
				err = fn(Change{Before: &was, After: &now})
			}
		}
		if err != nil {
			return err
		}
	}
}

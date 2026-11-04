package layeredfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type osFileReadHandle struct {
	file *os.File
}

func (h *osFileReadHandle) Read(p []byte) (int, error) {
	return h.file.Read(p)
}

func (h *osFileReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.file.ReadAt(p, off)
}

func (h *osFileReadHandle) Seek(offset int64, whence int) (int64, error) {
	return h.file.Seek(offset, whence)
}

func (h *osFileReadHandle) Close() error {
	if h == nil || h.file == nil {
		return nil
	}
	return h.file.Close()
}

// MemoryParentLayer is an in-memory and backing-file ParentLayer implementation
// for retained parent checkpoints (#1663, #1666).
type MemoryParentLayer struct {
	id      string
	mu      sync.RWMutex
	entries map[QualifiedPath]ParentEntry
}

// NewMemoryParentLayer creates a retained parent checkpoint layer with the given ID.
func NewMemoryParentLayer(id string) *MemoryParentLayer {
	return &MemoryParentLayer{
		id:      id,
		entries: make(map[QualifiedPath]ParentEntry),
	}
}

// ID returns the parent checkpoint ID.
func (l *MemoryParentLayer) ID() string {
	return l.id
}

// SetEntry records or updates an entry in the parent layer.
func (l *MemoryParentLayer) SetEntry(qpath QualifiedPath, entry ParentEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	qpath.Rel = cleanRelPath(qpath.Rel)
	if len(entry.Xattrs) > 0 {
		cp := make(map[string][]byte, len(entry.Xattrs))
		for k, v := range entry.Xattrs {
			cp[k] = append([]byte(nil), v...)
		}
		entry.Xattrs = cp
	}
	l.entries[qpath] = entry
}

// Lookup resolves a qualified path in the parent checkpoint layer.
func (l *MemoryParentLayer) Lookup(qpath QualifiedPath) (ParentEntry, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	qpath.Rel = cleanRelPath(qpath.Rel)
	entry, ok := l.entries[qpath]
	if !ok {
		return ParentEntry{}, false, nil
	}
	return cloneParentEntry(entry), true, nil
}

// OpenContent opens the immutable backing file for a regular file in the parent checkpoint.
func (l *MemoryParentLayer) OpenContent(qpath QualifiedPath) (*osFileReadHandle, error) {
	l.mu.RLock()
	qpath.Rel = cleanRelPath(qpath.Rel)
	entry, ok := l.entries[qpath]
	l.mu.RUnlock()
	if !ok || entry.Kind != EntryRegular {
		return nil, os.ErrNotExist
	}
	if entry.BackingAbs == "" {
		return nil, fmt.Errorf("parent layer %s missing backing file for %v", l.id, qpath)
	}
	f, err := os.Open(entry.BackingAbs)
	if err != nil {
		return nil, err
	}
	return &osFileReadHandle{file: f}, nil
}

// ListDir returns the immediate child entries recorded under dir in this parent layer.
func (l *MemoryParentLayer) ListDir(dir QualifiedPath) (map[string]ParentEntry, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	dirRel := cleanRelPath(dir.Rel)
	prefix := ""
	if dirRel != "." {
		prefix = dirRel + "/"
	}
	out := make(map[string]ParentEntry)
	for qp, entry := range l.entries {
		if qp.RootID != dir.RootID {
			continue
		}
		if qp.Rel == "." || qp.Rel == dirRel {
			continue
		}
		var rest string
		if prefix == "" {
			rest = qp.Rel
		} else {
			if !strings.HasPrefix(qp.Rel, prefix) {
				continue
			}
			rest = strings.TrimPrefix(qp.Rel, prefix)
		}
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		out[rest] = cloneParentEntry(entry)
	}
	return out, nil
}

func cloneParentEntry(e ParentEntry) ParentEntry {
	if len(e.Xattrs) > 0 {
		cp := make(map[string][]byte, len(e.Xattrs))
		for k, v := range e.Xattrs {
			cp[k] = append([]byte(nil), v...)
		}
		e.Xattrs = cp
	}
	return e
}

func cleanRelPath(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "/" {
		return "."
	}
	return rel
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// RegisterLowerProvider registers a custom or virtual LowerProvider for a RootID.
func (c *Core) RegisterLowerProvider(rootID string, provider LowerProvider) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	if _, ok := c.roots.byID[rootID]; !ok {
		return ErrInvalidRoot
	}
	c.roots.providers[rootID] = provider
	return nil
}

// ViewID returns the unique view identifier.
func (c *Core) ViewID() string {
	return c.cfg.ViewID
}

// Stats returns a snapshot of the deterministic operation and byte counters.
func (c *Core) Stats() Stats {
	c.mu.RLock()
	logical := countLogicalChangesLocked(c)
	c.mu.RUnlock()
	return Stats{
		LowerDirReads:     c.counters.lowerDirReads.Load(),
		LowerLookups:      c.counters.lowerLookups.Load(),
		PhysicalCopyUps:   c.counters.physicalCopyUps.Load(),
		LogicalChanges:    int64(logical),
		CapturedBaseBytes: c.counters.capturedBaseBytes.Load(),
		JournalEntries:    c.counters.journalEntries.Load(),
	}
}

// ResetStepStats clears per-scenario lower read/lookup counters while preserving view state.
func (c *Core) ResetStepStats() {
	c.counters.lowerDirReads.Store(0)
	c.counters.lowerLookups.Store(0)
}

func ensurePrivateDirNodeLocked(c *Core, qpath QualifiedPath, attr EntryAttr) *privateNode {
	if nid, ok := c.state.pathIndex[qpath]; ok {
		if n := c.state.nodesByID[nid]; n != nil && !n.flags.unlinked {
			return n
		}
	}
	nid, gen := c.state.allocNodeID()
	node := &privateNode{
		id:     nid,
		gen:    gen,
		kind:   EntryDirectory,
		mode:   attr.Mode,
		atime:  attr.ATime,
		mtime:  attr.ModTime,
		ctime:  attr.CTime,
		xattrs: cloneXattrs(attr.Xattrs),
		paths:  map[QualifiedPath]struct{}{qpath: {}},
	}
	c.state.nodesByID[nid] = node
	c.state.pathIndex[qpath] = nid
	if attr.Layer != LayerPrivate {
		recordOriginLocked(c, node, attr.NodeID)
	}
	return node
}

func ensureCopiedUpNode(c *Core, qpath QualifiedPath, expectedGen uint64) (*privateNode, error) {
	res, err := lookupExactComponent(c, qpath)
	if err != nil {
		return nil, err
	}
	base, err := ensureBaseCaptured(c, qpath)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return nil, ErrViewReleased
	}
	if c.state.nsGen != expectedGen {
		return nil, errNamespaceRetry
	}
	if nid, ok := c.state.pathIndex[qpath]; ok {
		if n := c.state.nodesByID[nid]; n != nil && !n.flags.unlinked {
			return n, nil
		}
	}
	switch res.attr.Kind {
	case EntryRegular:
		return ensureWritableNodeLocked(c, qpath, base, OpenWriteOnly, res.attr.Mode, expectedGen)
	case EntryDirectory:
		return ensurePrivateDirNodeLocked(c, qpath, res.attr), nil
	case EntrySymlink:
		nid, gen := c.state.allocNodeID()
		originCopy := qpath
		node := &privateNode{
			id:     nid,
			gen:    gen,
			kind:   EntrySymlink,
			mode:   res.attr.Mode,
			size:   int64(len(res.attr.LinkTarget)),
			atime:  res.attr.ATime,
			mtime:  res.attr.ModTime,
			ctime:  res.attr.CTime,
			xattrs: cloneXattrs(res.attr.Xattrs),
			paths:  map[QualifiedPath]struct{}{qpath: {}},
			flags: nodeFlags{
				physicalCopyUp: true,
				linkTarget:     res.attr.LinkTarget,
			},
			initial: nodeInitialState{
				mode:       res.attr.Mode,
				mtime:      res.attr.ModTime,
				xattrs:     cloneXattrs(res.attr.Xattrs),
				originPath: &originCopy,
			},
		}
		c.state.nodesByID[nid] = node
		c.state.pathIndex[qpath] = nid
		recordOriginLocked(c, node, res.attr.NodeID)
		c.counters.physicalCopyUps.Add(1)
		return node, nil
	default:
		return nil, fmt.Errorf("unsupported entry kind %s", res.attr.Kind)
	}
}

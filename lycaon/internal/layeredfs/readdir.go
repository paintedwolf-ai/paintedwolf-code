package layeredfs

import (
	"os"
	"strings"
	"syscall"
)

// ReadDir returns a merged, sorted directory listing for qpath across private, parent, and trunk layers.
func (c *Core) ReadDir(qpath QualifiedPath) ([]DirEntry, error) {
	res, err := resolvePath(c, qpath, true, false)
	if err != nil {
		return nil, err
	}
	if res.attr.Kind != EntryDirectory {
		return nil, syscall.ENOTDIR
	}
	entries, err := readDirUnlocked(c, res.qpath)
	if err != nil {
		return nil, err
	}
	emitObservation(c, ObservationEvent{
		Path:       res.qpath,
		Layer:      res.layer,
		NodeID:     res.attr.NodeID,
		Generation: res.attr.Generation,
		Kind:       ObserveReadDir,
	})
	return entries, nil
}

func readDirUnlocked(c *Core, dirPath QualifiedPath) ([]DirEntry, error) {
	dirPath.Rel = cleanRelPath(dirPath.Rel)
	merged := make(map[string]DirEntry)

	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return nil, ErrViewReleased
	}
	opaquePrivate := isMaskedByPrivateDeletionOrReplacementLocked(c, dirPath)
	if nid, ok := c.state.pathIndex[dirPath]; ok {
		if dn := c.state.nodesByID[nid]; dn != nil && dn.flags.namespaceAdded {
			opaquePrivate = true
		}
	}
	parentLayers := c.cfg.ParentLayers
	provider := c.roots.providers[dirPath.RootID]
	c.mu.RUnlock()

	if !opaquePrivate && provider != nil {
		c.counters.lowerDirReads.Add(1)
		lowerList, err := provider.ReadDir(dirPath.Rel)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, le := range lowerList {
			childPath := QualifiedPath{RootID: dirPath.RootID, Rel: joinRel(dirPath.Rel, le.Name)}
			merged[le.Name] = DirEntry{
				Name:       le.Name,
				NodeID:     syntheticNodeID(childPath, le.Ino),
				Generation: 1,
				Kind:       le.Kind,
				Mode:       le.Mode,
				Layer:      LayerTrunk,
			}
		}
	}

	if !opaquePrivate {
		for i := len(parentLayers) - 1; i >= 0; i-- {
			pl := parentLayers[i]
			pEntries, err := pl.ListDir(dirPath)
			if err != nil {
				return nil, err
			}
			for name, pe := range pEntries {
				if pe.Kind == EntryAbsent {
					delete(merged, name)
					continue
				}
				childPath := QualifiedPath{RootID: dirPath.RootID, Rel: joinRel(dirPath.Rel, name)}
				merged[name] = DirEntry{
					Name:       name,
					NodeID:     syntheticNodeID(childPath, 1),
					Generation: 1,
					Kind:       pe.Kind,
					Mode:       pe.Mode,
					Size:       pe.Size,
					ModTime:    pe.ModTime,
					Layer:      LayerParent,
				}
			}
		}
	}

	c.mu.RLock()
	prefix := ""
	if dirPath.Rel != "." {
		prefix = dirPath.Rel + "/"
	}
	for delPath := range c.state.deletedPaths {
		if name, ok := immediateChildName(dirPath.RootID, prefix, delPath); ok {
			delete(merged, name)
		}
	}
	for privPath, nid := range c.state.pathIndex {
		name, ok := immediateChildName(dirPath.RootID, prefix, privPath)
		if !ok {
			continue
		}
		node := c.state.nodesByID[nid]
		if node == nil || node.flags.unlinked {
			continue
		}
		merged[name] = DirEntry{
			Name:       name,
			NodeID:     node.id,
			Generation: node.gen,
			Kind:       node.kind,
			Mode:       node.mode,
			Size:       node.size,
			ModTime:    node.mtime,
			Layer:      LayerPrivate,
		}
	}
	c.mu.RUnlock()

	names := sortedNames(merged)
	out := make([]DirEntry, 0, len(names))
	for _, name := range names {
		out = append(out, merged[name])
	}
	return out, nil
}

func immediateChildName(rootID, prefix string, candidate QualifiedPath) (string, bool) {
	if candidate.RootID != rootID || candidate.Rel == "." {
		return "", false
	}
	var rest string
	if prefix == "" {
		rest = candidate.Rel
	} else {
		if !strings.HasPrefix(candidate.Rel, prefix) {
			return "", false
		}
		rest = strings.TrimPrefix(candidate.Rel, prefix)
	}
	if rest == "" || strings.IndexByte(rest, '/') >= 0 {
		return "", false
	}
	return rest, true
}

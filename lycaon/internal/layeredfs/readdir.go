package layeredfs

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// ReadDir returns a merged, sorted directory listing for qpath across private, parent, and trunk layers.
func (c *Core) ReadDir(qpath QualifiedPath) ([]DirEntry, error) {
	if err := c.barrier.enterOp(); err != nil {
		return nil, err
	}
	defer c.barrier.leaveOp()
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
	// Entries merge by the name the root folds them to, and a private spelling wins.
	folding := c.foldingOf(dirPath.RootID)
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
			// Lower FIFOs, sockets and devices have no identity in the view: Lstat reports them
			// absent, as a materialized seed omits them, so listing them would name a ghost.
			if le.Kind == EntryAbsent {
				continue
			}
			childPath := QualifiedPath{RootID: dirPath.RootID, Rel: joinRel(dirPath.Rel, le.Name)}
			merged[folding.key(le.Name)] = DirEntry{
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
					delete(merged, folding.key(name))
					continue
				}
				childPath := QualifiedPath{RootID: dirPath.RootID, Rel: joinRel(dirPath.Rel, name)}
				merged[folding.key(name)] = DirEntry{
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

	c.mu.Lock()
	prefix := ""
	if dirPath.Rel != "." {
		prefix = folding.relKey(dirPath.Rel) + "/"
	}
	for delPath := range c.state.deletedPaths {
		if name, ok := immediateChildName(dirPath.RootID, prefix, folding, delPath); ok {
			delete(merged, folding.key(name))
		}
	}
	var gitChildren []QualifiedPath
	for privPath, nid := range c.state.pathIndex {
		if _, ok := immediateChildName(dirPath.RootID, prefix, folding, privPath); ok {
			if node := c.state.nodesByID[nid]; node != nil && !node.flags.unlinked && node.flags.gitHostPath != "" {
				gitChildren = append(gitChildren, privPath)
			}
		}
	}
	for _, privPath := range gitChildren {
		if nid, ok := c.state.pathIndex[privPath]; ok {
			if node := c.state.nodesByID[nid]; node != nil && !node.flags.unlinked {
				refreshMutableGitNodeLocked(c, privPath, node)
			}
		}
	}
	for privPath, nid := range c.state.pathIndex {
		name, ok := immediateChildName(dirPath.RootID, prefix, folding, privPath)
		if !ok {
			continue
		}
		node := c.state.nodesByID[nid]
		if node == nil || node.flags.unlinked {
			continue
		}
		merged[folding.key(name)] = DirEntry{
			Name:       name,
			NodeID:     node.id,
			OriginID:   node.origin,
			Generation: node.gen,
			Kind:       node.kind,
			Mode:       node.mode,
			Size:       node.size,
			ModTime:    node.mtime,
			Layer:      LayerPrivate,
		}
	}
	c.mu.Unlock()

	names := sortedNames(merged)
	out := make([]DirEntry, 0, len(names))
	for _, name := range names {
		out = append(out, merged[name])
	}
	return out, nil
}

// immediateChildName is candidate's name when it sits directly in the directory whose folded path
// prefix names, with its own spelling.
func immediateChildName(rootID, prefix string, folding NameFolding, candidate QualifiedPath) (string, bool) {
	if candidate.RootID != rootID || candidate.Rel == "." {
		return "", false
	}
	dir, name := ".", candidate.Rel
	if slash := strings.LastIndexByte(candidate.Rel, '/'); slash >= 0 {
		dir, name = candidate.Rel[:slash], candidate.Rel[slash+1:]
	}
	if name == "" {
		return "", false
	}
	if prefix == "" {
		return name, dir == "."
	}
	return name, dir != "." && folding.relKey(dir)+"/" == prefix
}

func isMaskedByPrivateDeletionOrReplacementLocked(c *Core, qpath QualifiedPath) bool {
	rel := qpath.Rel
	for rel != "." {
		spelled := c.storedSpelling(QualifiedPath{RootID: qpath.RootID, Rel: rel})
		if _, del := c.state.deletedPaths[spelled]; del {
			return true
		}
		if ancID, ok := c.state.pathIndex[spelled]; ok {
			anc := c.state.nodesByID[ancID]
			if anc != nil && (anc.kind != EntryDirectory || anc.flags.namespaceAdded) {
				return true
			}
		}
		slash := strings.LastIndexByte(rel, '/')
		if slash < 0 {
			break
		}
		rel = rel[:slash]
	}
	return false
}

var errNamespaceRetry = errors.New("layeredfs: namespace generation changed; retry transaction")

func currentNamespaceGen(c *Core) (uint64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state.released {
		return 0, ErrViewReleased
	}
	return c.state.nsGen, nil
}

func runNamespacePreCommitHook(c *Core, op string, qpath QualifiedPath) {
	if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.NamespacePreCommitHook != nil {
		c.cfg.FaultHooks.NamespacePreCommitHook(op, qpath)
	}
}

func isParentDirValidLocked(c *Core, qpath QualifiedPath) bool {
	slash := strings.LastIndexByte(qpath.Rel, '/')
	rel := "."
	if slash >= 0 {
		rel = qpath.Rel[:slash]
	}
	for {
		anc := QualifiedPath{RootID: qpath.RootID, Rel: rel}
		if _, del := c.state.deletedPaths[anc]; del {
			return false
		}
		if nid, ok := c.state.pathIndex[anc]; ok {
			n := c.state.nodesByID[nid]
			if n == nil || n.flags.unlinked || n.kind != EntryDirectory {
				return false
			}
		}
		if rel == "." {
			break
		}
		slash = strings.LastIndexByte(rel, '/')
		if slash < 0 {
			rel = "."
		} else {
			rel = rel[:slash]
		}
	}
	return true
}

func hasPrivateChildrenLocked(c *Core, dirPath QualifiedPath) bool {
	prefix := ""
	if dirPath.Rel != "." {
		prefix = dirPath.Rel + "/"
	}
	for qp, nid := range c.state.pathIndex {
		if qp.RootID != dirPath.RootID || qp.Rel == "." || qp.Rel == dirPath.Rel {
			continue
		}
		if prefix == "" || strings.HasPrefix(qp.Rel, prefix) {
			if n := c.state.nodesByID[nid]; n != nil && !n.flags.unlinked {
				return true
			}
		}
	}
	return false
}

package layeredfs

import (
	"io/fs"
	"os"
	"syscall"
	"time"
)

// Mknod creates a private FIFO or socket node at qpath. Only the node is stored: a FIFO's buffer
// and a bound socket live in the kernel. Device nodes need privilege a worker never has.
func (c *Core) Mknod(qpath QualifiedPath, mode fs.FileMode) (EntryAttr, error) {
	var kind EntryKind
	switch {
	case mode&fs.ModeNamedPipe != 0:
		kind = EntryFIFO
	case mode&fs.ModeSocket != 0:
		kind = EntrySocket
	case mode&fs.ModeDevice != 0:
		return EntryAttr{}, syscall.EPERM
	default:
		return EntryAttr{}, syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()
	for {
		startGen, err := currentNamespaceGen(c)
		if err != nil {
			return EntryAttr{}, err
		}
		parentPath, name, err := resolveParentDir(c, qpath, true)
		if err != nil {
			return EntryAttr{}, err
		}
		targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
		targetPath, err = checkGitAccess(c, targetPath, true)
		if err != nil {
			return EntryAttr{}, err
		}
		if _, lookErr := lookupExactComponent(c, targetPath); lookErr == nil {
			return EntryAttr{}, os.ErrExist
		} else if !os.IsNotExist(lookErr) {
			return EntryAttr{}, lookErr
		}
		if _, err := ensureBaseCaptured(c, targetPath); err != nil {
			return EntryAttr{}, err
		}
		runNamespacePreCommitHook(c, "mknod", targetPath)

		c.mu.Lock()
		if c.state.released {
			c.mu.Unlock()
			return EntryAttr{}, ErrViewReleased
		}
		if c.state.nsGen != startGen || !isParentDirValidLocked(c, targetPath) {
			c.mu.Unlock()
			continue
		}
		if _, exists := c.state.pathIndex[targetPath]; exists {
			c.mu.Unlock()
			return EntryAttr{}, os.ErrExist
		}
		now := time.Now()
		nid, gen := c.state.allocNodeID()
		node := &privateNode{
			id:    nid,
			gen:   gen,
			kind:  kind,
			mode:  (mode & fs.ModeType) | mode.Perm(),
			atime: now,
			mtime: now,
			ctime: now,
			paths: map[QualifiedPath]struct{}{targetPath: {}},
			flags: nodeFlags{namespaceAdded: true},
		}
		delete(c.state.deletedPaths, targetPath)
		c.state.nodesByID[nid] = node
		c.state.pathIndex[targetPath] = nid
		bumpParentDirGenLocked(c, targetPath)
		recordJournalLocked(c, JournalOpMknod, targetPath, nil, node, 0)
		attr := nodeToAttrLocked(node)
		c.mu.Unlock()
		return attr, nil
	}
}

// SetAttrNode applies attributes to a private node by identity, for a descriptor whose object has
// no usable path: unlinked, or replaced so its old name now belongs to another file. It returns
// ErrStaleHandle when the node is gone or the generation differs. A journal record for a node with
// no remaining path carries an empty path, since nothing checkpointable changed.
func (c *Core) SetAttrNode(nodeID NodeID, gen Generation, req SetAttrRequest) (EntryAttr, error) {
	if req.UID != nil || req.GID != nil {
		return EntryAttr{}, syscall.EPERM
	}
	if req.Size != nil && *req.Size < 0 {
		return EntryAttr{}, syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	lookup := func() (*privateNode, error) {
		if c.state.released {
			return nil, ErrViewReleased
		}
		node, ok := c.state.nodesByID[nodeID]
		if !ok || node.gen != gen || (node.flags.unlinked && !hasOpenHandlesForNodeLocked(c, nodeID)) {
			return nil, ErrStaleHandle
		}
		return node, nil
	}
	c.mu.RLock()
	node, err := lookup()
	c.mu.RUnlock()
	if err != nil {
		return EntryAttr{}, err
	}
	if req.Size != nil && node.kind != EntryRegular {
		if node.kind == EntryDirectory {
			return EntryAttr{}, syscall.EISDIR
		}
		return EntryAttr{}, syscall.EINVAL
	}
	if req.Size != nil {
		node.flags.ioMu.Lock()
		defer node.flags.ioMu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := lookup(); err != nil {
		return EntryAttr{}, err
	}
	qpath := anyNodePathLocked(node)
	if req.Size != nil {
		wf, oErr := openBackingFileRW(int(c.backing.workDirFile.Fd()), node.flags.backingName, node.flags.backingPath)
		if oErr != nil {
			return EntryAttr{}, oErr
		}
		tErr := wf.Truncate(*req.Size)
		_ = wf.Close()
		if tErr != nil {
			return EntryAttr{}, tErr
		}
		now := time.Now()
		node.size, node.mtime, node.ctime = *req.Size, now, now
		node.flags.contentChanged = true
		if node.flags.backingPath != "" {
			c.state.dirtyBackingFiles[node.flags.backingPath] = struct{}{}
		}
		recordJournalLocked(c, JournalOpTruncate, qpath, nil, node, *req.Size)
	}
	if err := applyAttrChangesLocked(c, node, qpath, req); err != nil {
		return EntryAttr{}, err
	}
	return nodeToAttrLocked(node), nil
}

// ResolveIdentity finds the live private object for a stable identity, including a copied-up
// object still addressed by the identity of its lower source.
func (c *Core) ResolveIdentity(id NodeID) (QualifiedPath, EntryAttr, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state.released {
		return QualifiedPath{}, EntryAttr{}, false
	}
	nid := id
	if mapped, ok := c.state.originIndex[id]; ok {
		nid = mapped
	}
	node, ok := c.state.nodesByID[nid]
	if !ok || node.flags.unlinked || len(node.paths) == 0 {
		return QualifiedPath{}, EntryAttr{}, false
	}
	return anyNodePathLocked(node), nodeToAttrLocked(node), true
}

// FindChildByIdentity names the entry of dir with the given stable identity. It lists the directory
// without recording an observation, because no worker asked for the listing.
func (c *Core) FindChildByIdentity(dir QualifiedPath, id NodeID) (string, bool, error) {
	if err := c.barrier.enterOp(); err != nil {
		return "", false, err
	}
	defer c.barrier.leaveOp()
	entries, err := readDirUnlocked(c, dir)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		if e.StableID() == id {
			return e.Name, true, nil
		}
	}
	return "", false, nil
}

// AdoptIdentity gives the private object at qpath a stable identity a protocol handle already holds,
// for an object the adapter presented before it existed in the view.
func (c *Core) AdoptIdentity(qpath QualifiedPath, id NodeID) {
	qpath.Rel = cleanRelPath(qpath.Rel)
	c.mu.Lock()
	defer c.mu.Unlock()
	if nid, ok := c.state.pathIndex[qpath]; ok && !c.state.released {
		if node := c.state.nodesByID[nid]; node != nil {
			recordOriginLocked(c, node, id)
		}
	}
}

// MarkTransientName records that qpath is a protocol client's placeholder for an open file it was
// asked to delete. The object stays reachable in the live view while checkpoints treat the path as
// absent; deleting or renaming the path clears the mark.
func (c *Core) MarkTransientName(qpath QualifiedPath) {
	qpath.Rel = cleanRelPath(qpath.Rel)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.state.pathIndex[qpath]; ok && !c.state.released {
		c.state.transient[qpath] = struct{}{}
	}
}

func isContentlessPathLocked(c *Core, qp QualifiedPath, node *privateNode) bool {
	if node.kind.IsSpecial() {
		return true
	}
	_, hidden := c.state.transient[qp]
	return hidden
}

func anyNodePathLocked(node *privateNode) QualifiedPath {
	var best QualifiedPath
	for qp := range node.paths {
		if best == (QualifiedPath{}) || qp.RootID < best.RootID || (qp.RootID == best.RootID && qp.Rel < best.Rel) {
			best = qp
		}
	}
	return best
}

func originNodeID(qpath QualifiedPath, base BaseCapture) NodeID {
	if base.Layer == LayerParent {
		return syntheticNodeID(qpath, 1)
	}
	return syntheticNodeID(qpath, base.Ino)
}

func recordOriginLocked(c *Core, node *privateNode, origin NodeID) {
	node.origin = origin
	c.state.originIndex[origin] = node.id
}

// reclaimNodeLocked drops an unlinked node once no handle can reach it, so removing files does not
// grow view state. The journal keeps what recovery needs.
func reclaimNodeLocked(c *Core, node *privateNode) {
	if node.flags.backingPath != "" {
		delete(c.state.dirtyBackingFiles, node.flags.backingPath)
	}
	delete(c.state.nodesByID, node.id)
	if node.origin != 0 && c.state.originIndex[node.origin] == node.id {
		delete(c.state.originIndex, node.origin)
	}
}

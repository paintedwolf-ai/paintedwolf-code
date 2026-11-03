package layeredfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func ensureWritableNodeLocked(c *Core, qpath QualifiedPath, base BaseCapture, flags OpenFlags, createMode os.FileMode, expectedGen uint64) (*privateNode, error) {
	if c.state.nsGen != expectedGen {
		return nil, errNamespaceRetry
	}
	if existingID, ok := c.state.pathIndex[qpath]; ok {
		if existing := c.state.nodesByID[existingID]; existing != nil && !existing.flags.unlinked {
			return existing, nil
		}
	}

	_, wasDeleted := c.state.deletedPaths[qpath]
	if !wasDeleted && base.Kind == EntryRegular {
		c.mu.Unlock()
		workName, workPath, err := initWorkingFromBase(c, base)
		c.mu.Lock()
		if err != nil {
			return nil, err
		}
		if c.state.released {
			_ = unlinkBackingFile(int(c.backing.workDirFile.Fd()), workName)
			return nil, ErrViewReleased
		}
		if c.state.nsGen != expectedGen {
			_ = unlinkBackingFile(int(c.backing.workDirFile.Fd()), workName)
			return nil, errNamespaceRetry
		}
		if existingID, ok := c.state.pathIndex[qpath]; ok {
			if existing := c.state.nodesByID[existingID]; existing != nil && !existing.flags.unlinked {
				_ = unlinkBackingFile(int(c.backing.workDirFile.Fd()), workName)
				return existing, nil
			}
		}
		nid, gen := c.state.allocNodeID()
		originCopy := qpath
		node := &privateNode{
			id:     nid,
			gen:    gen,
			kind:   EntryRegular,
			mode:   base.Mode,
			size:   base.Size,
			atime:  base.ModTime,
			mtime:  base.ModTime,
			ctime:  base.ModTime,
			xattrs: cloneXattrs(base.Xattrs),
			paths:  map[QualifiedPath]struct{}{qpath: {}},
			flags: nodeFlags{
				physicalCopyUp: true,
				backingName:    workName,
				backingPath:    workPath,
			},
			initial: nodeInitialState{
				sha256:     base.SHA256,
				size:       base.Size,
				mode:       base.Mode,
				mtime:      base.ModTime,
				xattrs:     cloneXattrs(base.Xattrs),
				originPath: &originCopy,
			},
		}
		c.state.nodesByID[nid] = node
		c.state.pathIndex[qpath] = nid
		c.counters.physicalCopyUps.Add(1)
		return node, nil
	}

	if flags&OpenCreate == 0 {
		return nil, os.ErrNotExist
	}
	if !isParentDirValidLocked(c, qpath) {
		return nil, errNamespaceRetry
	}

	workName, workPath := c.state.allocStorageFile(c.backing.workDir, "work")
	workDirFD := int(c.backing.workDirFile.Fd())
	wf, err := createExclusiveBackingFile(workDirFD, workName, workPath, 0o600)
	if err != nil {
		return nil, err
	}
	_ = wf.Close()
	_ = syncFD(workDirFD)

	now := time.Now()
	nid, gen := c.state.allocNodeID()
	cleanMode := createMode.Perm() | (createMode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
	if cleanMode == 0 {
		cleanMode = 0o644
	}
	node := &privateNode{
		id:    nid,
		gen:   gen,
		kind:  EntryRegular,
		mode:  cleanMode,
		atime: now,
		mtime: now,
		ctime: now,
		paths: map[QualifiedPath]struct{}{qpath: {}},
		flags: nodeFlags{
			contentChanged: true,
			namespaceAdded: true,
			ownerCreatedRO: cleanMode&0o200 == 0,
			backingName:    workName,
			backingPath:    workPath,
		},
	}
	delete(c.state.deletedPaths, qpath)
	c.state.nodesByID[nid] = node
	c.state.pathIndex[qpath] = nid
	bumpParentDirGenLocked(c, qpath)
	recordJournalLocked(c, JournalOpCreate, qpath, nil, node, 0)
	return node, nil
}

func joinRel(parentRel, name string) string {
	parentRel = cleanRelPath(parentRel)
	if parentRel == "." {
		return name
	}
	return filepath.ToSlash(parentRel + "/" + name)
}

// WriteViaNodeHandle supports protocol-level writes (like NFSv3 WRITE on an owner-created 0444 file)
// identified by (NodeID, Generation) without requiring a stateful open handle.
func (c *Core) WriteViaNodeHandle(nodeID NodeID, gen Generation, data []byte, offset int64) (int, EntryAttr, error) {
	if err := c.barrier.enterMutation(); err != nil {
		return 0, EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return 0, EntryAttr{}, ErrViewReleased
	}
	node, ok := c.state.nodesByID[nodeID]
	if !ok || node == nil || node.gen != gen || node.flags.unlinked {
		c.mu.RUnlock()
		return 0, EntryAttr{}, ErrStaleHandle
	}
	c.mu.RUnlock()

	node.flags.ioMu.Lock()
	defer node.flags.ioMu.Unlock()

	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return 0, EntryAttr{}, ErrViewReleased
	}
	if node.gen != gen || node.flags.unlinked {
		c.mu.RUnlock()
		return 0, EntryAttr{}, ErrStaleHandle
	}
	if node.kind != EntryRegular {
		c.mu.RUnlock()
		return 0, EntryAttr{}, syscall.EISDIR
	}
	if node.mode&0o200 == 0 && !node.flags.ownerCreatedRO {
		c.mu.RUnlock()
		return 0, EntryAttr{}, syscall.EACCES
	}
	backingName, backingPath := node.flags.backingName, node.flags.backingPath
	var anyPath QualifiedPath
	for qp := range node.paths {
		anyPath = qp
		break
	}
	c.mu.RUnlock()

	wf, err := openBackingFileRW(int(c.backing.workDirFile.Fd()), backingName, backingPath)
	if err != nil {
		return 0, EntryAttr{}, err
	}
	n, wErr := writeBackingAt(c, anyPath, wf, data, offset)
	_ = wf.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	if n > 0 {
		if offset+int64(n) > node.size {
			node.size = offset + int64(n)
		}
		now := time.Now()
		node.mtime = now
		node.ctime = now
		node.flags.contentChanged = true
		for qp := range node.paths {
			anyPath = qp
			break
		}
		recordJournalLocked(c, JournalOpWrite, anyPath, nil, node, int64(n))
	}
	attr := nodeToAttrLocked(node)
	return n, attr, wErr
}

// OpenBackingReader opens a read-only stream to a private node's backing content by NodeID and Generation.
func (c *Core) OpenBackingReader(nodeID NodeID, gen Generation) (io.ReadCloser, error) {
	if err := c.barrier.enterOp(); err != nil {
		return nil, err
	}
	defer c.barrier.leaveOp()
	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return nil, ErrViewReleased
	}
	node, ok := c.state.nodesByID[nodeID]
	if !ok || node == nil || node.gen != gen || node.kind != EntryRegular {
		c.mu.RUnlock()
		return nil, ErrStaleHandle
	}
	bName, bPath := node.flags.backingName, node.flags.backingPath
	c.mu.RUnlock()
	return openBackingFileRO(int(c.backing.workDirFile.Fd()), bName, bPath)
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
		c.counters.physicalCopyUps.Add(1)
		return node, nil
	default:
		return nil, fmt.Errorf("unsupported entry kind %s", res.attr.Kind)
	}
}

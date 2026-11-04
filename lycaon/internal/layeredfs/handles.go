package layeredfs

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// ReadFile reads the entire contents of qpath from whichever layer owns it.
func (c *Core) ReadFile(qpath QualifiedPath) ([]byte, error) {
	if err := c.barrier.enterOp(); err != nil {
		return nil, err
	}
	defer c.barrier.leaveOp()
	hid, attr, err := openAdmitted(c, qpath, OpenReadOnly, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = closeHandleAdmitted(c, hid) }()
	if attr.Kind != EntryRegular {
		return nil, syscall.EISDIR
	}
	buf := make([]byte, attr.Size)
	if attr.Size == 0 {
		return buf, nil
	}
	n, rErr := readHandleAdmitted(c, hid, buf, 0)
	if rErr != nil && n == 0 {
		return nil, rErr
	}
	return buf[:n], nil
}

// Open resolves qpath and opens a view-scoped handle.
func (c *Core) Open(qpath QualifiedPath, flags OpenFlags, mode os.FileMode) (HandleID, EntryAttr, error) {
	if flags.hasWriteIntent() {
		if err := c.barrier.enterMutation(); err != nil {
			return 0, EntryAttr{}, err
		}
		defer c.barrier.leaveMutation()
	} else {
		if err := c.barrier.enterOp(); err != nil {
			return 0, EntryAttr{}, err
		}
		defer c.barrier.leaveOp()
	}
	return openAdmitted(c, qpath, flags, mode)
}

func openAdmitted(c *Core, qpath QualifiedPath, flags OpenFlags, mode os.FileMode) (HandleID, EntryAttr, error) {
	writeIntent := flags.hasWriteIntent()
	if !writeIntent {
		parentPath, name, err := resolveParentDir(c, qpath, false)
		if err != nil {
			if cleanRelPath(qpath.Rel) == "." {
				return openRootDirHandle(c, qpath)
			}
			return 0, EntryAttr{}, err
		}
		targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
		targetPath, err = checkGitAccess(c, targetPath, false)
		if err != nil {
			return 0, EntryAttr{}, err
		}
		return openReadOnlyTarget(c, targetPath, flags)
	}
	for {
		startGen, err := currentNamespaceGen(c)
		if err != nil {
			return 0, EntryAttr{}, err
		}
		parentPath, name, err := resolveParentDir(c, qpath, true)
		if err != nil {
			return 0, EntryAttr{}, err
		}
		targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
		targetPath, err = checkGitAccess(c, targetPath, true)
		if err != nil {
			return 0, EntryAttr{}, err
		}
		hid, attr, err := openMutableTarget(c, targetPath, flags, mode, startGen)
		if errors.Is(err, errNamespaceRetry) {
			continue
		}
		return hid, attr, err
	}
}

func openRootDirHandle(c *Core, qpath QualifiedPath) (HandleID, EntryAttr, error) {
	qpath.Rel = "."
	res, err := lookupExactComponent(c, qpath)
	if err != nil {
		return 0, EntryAttr{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return 0, EntryAttr{}, ErrViewReleased
	}
	node := res.privNode
	if node == nil {
		node = ensurePrivateDirNodeLocked(c, qpath, res.attr)
	}
	hid := c.state.allocHandleID()
	c.state.handles[hid] = &openHandle{
		id:     hid,
		viewID: c.cfg.ViewID,
		nodeID: node.id,
		gen:    node.gen,
		qpath:  qpath,
		flags:  OpenDirectory,
		isDir:  true,
		refs:   1,
	}
	return hid, nodeToAttrLocked(node), nil
}

func openReadOnlyTarget(c *Core, targetPath QualifiedPath, flags OpenFlags) (HandleID, EntryAttr, error) {
	followFinal := flags&OpenNoFollow == 0
	res, err := resolvePath(c, targetPath, followFinal, false)
	if err != nil {
		return 0, EntryAttr{}, err
	}
	if res.attr.Kind == EntrySymlink {
		return 0, EntryAttr{}, syscall.ELOOP
	}
	if flags&OpenDirectory != 0 && res.attr.Kind != EntryDirectory {
		return 0, EntryAttr{}, syscall.ENOTDIR
	}
	if res.attr.Kind.IsSpecial() {
		return 0, EntryAttr{}, ErrUnsupportedOperation
	}
	if res.attr.Kind == EntryDirectory {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.state.released {
			return 0, EntryAttr{}, ErrViewReleased
		}
		nodeID, gen := res.attr.NodeID, res.attr.Generation
		if res.privNode != nil {
			nodeID, gen = res.privNode.id, res.privNode.gen
		}
		hid := c.state.allocHandleID()
		c.state.handles[hid] = &openHandle{
			id:     hid,
			viewID: c.cfg.ViewID,
			nodeID: nodeID,
			gen:    gen,
			qpath:  res.qpath,
			flags:  flags,
			isDir:  true,
			refs:   1,
		}
		return hid, res.attr, nil
	}

	roReader, err := openReadOnlyStream(c, res)
	if err != nil {
		return 0, EntryAttr{}, err
	}

	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		_ = roReader.Close()
		return 0, EntryAttr{}, ErrViewReleased
	}
	hid := c.state.allocHandleID()
	c.state.handles[hid] = &openHandle{
		id:       hid,
		viewID:   c.cfg.ViewID,
		nodeID:   res.attr.NodeID,
		gen:      res.attr.Generation,
		qpath:    res.qpath,
		flags:    flags,
		refs:     1,
		roReader: roReader,
	}
	c.mu.Unlock()
	return hid, res.attr, nil
}

func openReadOnlyStream(c *Core, res resolvedLayerEntry) (ReadSeekCloser, error) {
	if res.layer == LayerPrivate && res.privNode != nil {
		f, err := openBackingFileRO(int(c.backing.workDirFile.Fd()), res.privNode.flags.backingName, res.privNode.flags.backingPath)
		if err != nil {
			return nil, err
		}
		return &osFileReadHandle{file: f}, nil
	}
	if res.layer == LayerParent {
		for _, pl := range c.cfg.ParentLayers {
			if _, ok, _ := pl.Lookup(res.qpath); ok {
				return pl.OpenContent(res.qpath)
			}
		}
		return nil, os.ErrNotExist
	}
	prov := c.roots.providers[res.qpath.RootID]
	if prov == nil {
		return nil, os.ErrNotExist
	}
	r, _, err := prov.OpenRead(res.qpath.Rel)
	return r, err
}

func openMutableTarget(c *Core, targetPath QualifiedPath, flags OpenFlags, mode os.FileMode, startGen uint64) (HandleID, EntryAttr, error) {
	followFinal := flags&OpenNoFollow == 0
	existingRes, lookErr := resolvePath(c, targetPath, followFinal, true)
	if lookErr == nil && existingRes.attr.Kind == EntrySymlink {
		return 0, EntryAttr{}, syscall.ELOOP
	}
	if lookErr == nil && existingRes.attr.Kind == EntryDirectory {
		return 0, EntryAttr{}, syscall.EISDIR
	}
	if lookErr == nil && flags&OpenCreate != 0 && flags&OpenExclusive != 0 {
		return 0, EntryAttr{}, os.ErrExist
	}
	if lookErr != nil && !os.IsNotExist(lookErr) {
		return 0, EntryAttr{}, lookErr
	}
	if os.IsNotExist(lookErr) && flags&OpenCreate == 0 {
		return 0, EntryAttr{}, os.ErrNotExist
	}

	effPath := targetPath
	if lookErr == nil {
		effPath = existingRes.qpath
	}

	base, err := ensureBaseCaptured(c, effPath)
	if err != nil {
		return 0, EntryAttr{}, err
	}

	runNamespacePreCommitHook(c, "open", effPath)

	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return 0, EntryAttr{}, ErrViewReleased
	}
	if c.state.nsGen != startGen || !isParentDirValidLocked(c, effPath) {
		c.mu.Unlock()
		return 0, EntryAttr{}, errNamespaceRetry
	}
	node, nErr := ensureWritableNodeLocked(c, effPath, base, flags, mode, startGen)
	c.mu.Unlock()
	if nErr != nil {
		return 0, EntryAttr{}, nErr
	}
	node.flags.ioMu.Lock()
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		node.flags.ioMu.Unlock()
		return 0, EntryAttr{}, ErrViewReleased
	}
	if node.flags.unlinked || c.state.pathIndex[effPath] != node.id {
		c.mu.Unlock()
		node.flags.ioMu.Unlock()
		return 0, EntryAttr{}, errNamespaceRetry
	}
	defer node.flags.ioMu.Unlock()
	defer c.mu.Unlock()
	if node.kind.IsSpecial() {
		return 0, EntryAttr{}, ErrUnsupportedOperation
	}
	wf, err := openBackingFileRW(int(c.backing.workDirFile.Fd()), node.flags.backingName, node.flags.backingPath)
	if err != nil {
		return 0, EntryAttr{}, err
	}

	if flags&OpenTruncate != 0 && node.size > 0 {
		if tErr := wf.Truncate(0); tErr != nil {
			_ = wf.Close()
			return 0, EntryAttr{}, tErr
		}
		node.size = 0
		node.mtime = time.Now()
		node.ctime = node.mtime
		node.flags.contentChanged = true
		recordJournalLocked(c, JournalOpTruncate, effPath, nil, node, 0)
	}

	hid := c.state.allocHandleID()
	c.state.handles[hid] = &openHandle{
		id:     hid,
		viewID: c.cfg.ViewID,
		nodeID: node.id,
		gen:    node.gen,
		qpath:  effPath,
		flags:  flags,
		refs:   1,
		file:   wf,
	}
	return hid, nodeToAttrLocked(node), nil
}

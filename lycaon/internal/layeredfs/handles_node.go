package layeredfs

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

func ensureWritableNodeLocked(c *Core, qpath QualifiedPath, base BaseCapture, flags OpenFlags, createMode os.FileMode, expectedGen uint64) (*privateNode, error) {
	if c.state.nsGen != expectedGen {
		return nil, errNamespaceRetry
	}
	if existingID, ok := c.state.pathIndex[qpath]; ok {
		if existing := c.state.nodesByID[existingID]; existing != nil && !existing.flags.unlinked {
			if refreshMutableGitNodeLocked(c, qpath, existing) {
				return existing, nil
			}
		}
	}

	if hostPath, isRepoRoot, isMutableGit, gitErr := resolveMutableGitTarget(c, qpath); gitErr != nil {
		return nil, gitErr
	} else if isMutableGit && !isRepoRoot {
		if !isParentDirValidLocked(c, qpath) {
			return nil, errNamespaceRetry
		}
		if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
			return nil, err
		}
		cleanMode := createMode.Perm() | (createMode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
		if cleanMode == 0 {
			cleanMode = 0o644
		}
		if flags&OpenCreate != 0 && flags&OpenExclusive != 0 {
			f, err := os.OpenFile(hostPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, cleanMode.Perm())
			if err != nil {
				return nil, err
			}
			info, _ := f.Stat()
			_ = f.Close()
			now := time.Now()
			if info != nil {
				now = info.ModTime()
			}
			nid, gen := c.state.allocNodeID()
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
					backingPath:    hostPath,
					gitHostPath:    hostPath,
				},
			}
			delete(c.state.deletedPaths, qpath)
			c.state.nodesByID[nid] = node
			c.state.pathIndex[qpath] = nid
			markDirtyBackingFileLocked(c, hostPath)
			if ino := fileInfoIno(info); ino != 0 {
				recordOriginLocked(c, node, syntheticNodeID(qpath, ino))
			}
			bumpParentDirGenLocked(c, qpath)
			recordJournalLocked(c, JournalOpCreate, qpath, nil, node, 0)
			return node, nil
		}

		info, statErr := os.Lstat(hostPath)
		if statErr == nil && info.Mode().IsRegular() {
			nid, gen := c.state.allocNodeID()
			originCopy := qpath
			node := &privateNode{
				id:     nid,
				gen:    gen,
				kind:   EntryRegular,
				mode:   info.Mode(),
				size:   info.Size(),
				atime:  info.ModTime(),
				mtime:  info.ModTime(),
				ctime:  info.ModTime(),
				xattrs: cloneXattrs(base.Xattrs),
				paths:  map[QualifiedPath]struct{}{qpath: {}},
				flags: nodeFlags{
					physicalCopyUp: true,
					backingPath:    hostPath,
					gitHostPath:    hostPath,
				},
				initial: nodeInitialState{
					sha256:     base.SHA256,
					size:       info.Size(),
					mode:       info.Mode(),
					mtime:      info.ModTime(),
					xattrs:     cloneXattrs(base.Xattrs),
					originPath: &originCopy,
				},
			}
			delete(c.state.deletedPaths, qpath)
			c.state.nodesByID[nid] = node
			c.state.pathIndex[qpath] = nid
			markDirtyBackingFileLocked(c, hostPath)
			ino := fileInfoIno(info)
			if ino == 0 {
				ino = base.Ino
			}
			recordOriginLocked(c, node, syntheticNodeID(qpath, ino))
			c.counters.physicalCopyUps.Add(1)
			return node, nil
		}
		if flags&OpenCreate == 0 {
			return nil, os.ErrNotExist
		}
		f, err := os.OpenFile(hostPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, cleanMode.Perm())
		if err != nil {
			return nil, err
		}
		cInfo, _ := f.Stat()
		_ = f.Close()
		now := time.Now()
		if cInfo != nil {
			now = cInfo.ModTime()
		}
		nid, gen := c.state.allocNodeID()
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
				backingPath:    hostPath,
				gitHostPath:    hostPath,
			},
		}
		delete(c.state.deletedPaths, qpath)
		c.state.nodesByID[nid] = node
		c.state.pathIndex[qpath] = nid
		markDirtyBackingFileLocked(c, hostPath)
		if ino := fileInfoIno(cInfo); ino != 0 {
			recordOriginLocked(c, node, syntheticNodeID(qpath, ino))
		}
		bumpParentDirGenLocked(c, qpath)
		recordJournalLocked(c, JournalOpCreate, qpath, nil, node, 0)
		return node, nil
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
		markDirtyBackingFileLocked(c, workPath)
		recordOriginLocked(c, node, originNodeID(qpath, base))
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
	markDirtyBackingFileLocked(c, workPath)

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

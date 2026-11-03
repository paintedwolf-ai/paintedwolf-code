package layeredfs

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

// Link creates a hard link at newPath sharing the exact private node identity of oldPath
// without scanning the project for aliases (#1657).
func (c *Core) Link(oldPath, newPath QualifiedPath) (EntryAttr, error) {
	return linkPath(c, oldPath, newPath)
}

func linkPath(c *Core, oldPath, newPath QualifiedPath) (EntryAttr, error) {
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	if oldPath.RootID != newPath.RootID {
		return EntryAttr{}, syscall.EXDEV
	}
	oldParent, oldName, err := resolveParentDir(c, oldPath, true)
	if err != nil {
		return EntryAttr{}, err
	}
	oldTarget := QualifiedPath{RootID: oldParent.RootID, Rel: joinRel(oldParent.Rel, oldName)}
	oldRes, err := lookupExactComponent(c, oldTarget)
	if err != nil {
		return EntryAttr{}, err
	}
	if oldRes.attr.Kind == EntryDirectory {
		return EntryAttr{}, syscall.EPERM
	}
	if oldRes.attr.Kind != EntryRegular {
		return EntryAttr{}, ErrUnsupportedOperation
	}

	newParent, newName, err := resolveParentDir(c, newPath, true)
	if err != nil {
		return EntryAttr{}, err
	}
	newTarget := QualifiedPath{RootID: newParent.RootID, Rel: joinRel(newParent.Rel, newName)}
	newTarget, err = checkGitAccess(c, newTarget, true)
	if err != nil {
		return EntryAttr{}, err
	}
	if _, lookErr := lookupExactComponent(c, newTarget); lookErr == nil {
		return EntryAttr{}, os.ErrExist
	} else if !os.IsNotExist(lookErr) {
		return EntryAttr{}, lookErr
	}

	oldBase, err := ensureBaseCaptured(c, oldTarget)
	if err != nil {
		return EntryAttr{}, err
	}
	if _, err := ensureBaseCaptured(c, newTarget); err != nil {
		return EntryAttr{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return EntryAttr{}, ErrViewReleased
	}
	node, err := ensureWritableNodeLocked(c, oldTarget, oldBase, OpenWriteOnly, oldRes.attr.Mode)
	if err != nil {
		return EntryAttr{}, err
	}
	node.paths[newTarget] = struct{}{}
	node.ctime = time.Now()
	node.flags.metadataChanged = true
	delete(c.state.deletedPaths, newTarget)
	c.state.pathIndex[newTarget] = node.id
	bumpParentDirGenLocked(c, newTarget)
	recordJournalLocked(c, JournalOpHardLink, newTarget, &oldTarget, node, 0)
	return nodeToAttrLocked(node), nil
}

// Unlink removes a file or symlink (or empty directory when removeDir is true).
// Open handles to an unlinked file remain valid for read/write/stat until closed (#1657).
func (c *Core) Unlink(qpath QualifiedPath, removeDir bool) error {
	return unlinkPath(c, qpath, removeDir)
}

func unlinkPath(c *Core, qpath QualifiedPath, removeDir bool) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()

	parentPath, name, err := resolveParentDir(c, qpath, true)
	if err != nil {
		return err
	}
	targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
	targetPath, err = checkGitAccess(c, targetPath, true)
	if err != nil {
		return err
	}

	res, err := lookupExactComponent(c, targetPath)
	if err != nil {
		return err
	}
	if removeDir && res.attr.Kind != EntryDirectory {
		return syscall.ENOTDIR
	}
	if !removeDir && res.attr.Kind == EntryDirectory {
		return syscall.EISDIR
	}
	if removeDir {
		entries, lErr := readDirUnlocked(c, targetPath)
		if lErr != nil {
			return lErr
		}
		if len(entries) > 0 {
			return syscall.ENOTEMPTY
		}
	}

	if _, err := ensureBaseCaptured(c, targetPath); err != nil {
		return err
	}

	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return ErrViewReleased
	}
	var removedNode *privateNode
	var orphanBacking string
	if nid, ok := c.state.pathIndex[targetPath]; ok {
		removedNode = c.state.nodesByID[nid]
		delete(c.state.pathIndex, targetPath)
		if removedNode != nil {
			delete(removedNode.paths, targetPath)
			removedNode.ctime = time.Now()
			if len(removedNode.paths) == 0 {
				removedNode.flags.unlinked = true
				if removedNode.flags.backingName != "" && !hasOpenHandlesForNodeLocked(c, removedNode.id) {
					orphanBacking = removedNode.flags.backingName
				}
			}
		}
	}
	c.state.deletedPaths[targetPath] = deletedState{
		ReplacedKind: res.attr.Kind,
		Timestamp:    time.Now(),
	}
	bumpParentDirGenLocked(c, targetPath)
	recordJournalLocked(c, JournalOpUnlink, targetPath, nil, removedNode, 0)
	var workDirFD = -1
	if orphanBacking != "" && c.backing.workDirFile != nil {
		workDirFD = int(c.backing.workDirFile.Fd())
	}
	c.mu.Unlock()
	if orphanBacking != "" && workDirFD >= 0 && removedNode != nil {
		removedNode.flags.ioMu.Lock()
		_ = unlinkBackingFile(workDirFD, orphanBacking)
		removedNode.flags.ioMu.Unlock()
	}
	return nil
}

// RemoveAll recursively removes a file or directory subtree, capturing bases only for the affected subtree (#1657).
func (c *Core) RemoveAll(qpath QualifiedPath) error {
	return removeAllPath(c, qpath)
}

func removeAllPath(c *Core, qpath QualifiedPath) error {
	res, err := resolvePath(c, qpath, false, true)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if res.attr.Kind != EntryDirectory {
		return unlinkPath(c, res.qpath, false)
	}
	children, err := c.ReadDir(res.qpath)
	if err != nil {
		return err
	}
	for _, child := range children {
		childPath := QualifiedPath{RootID: res.qpath.RootID, Rel: joinRel(res.qpath.Rel, child.Name)}
		if err := removeAllPath(c, childPath); err != nil {
			return err
		}
	}
	return unlinkPath(c, res.qpath, true)
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

func bumpParentDirGenLocked(c *Core, qpath QualifiedPath) {
	slash := strings.LastIndexByte(qpath.Rel, '/')
	parentRel := "."
	if slash >= 0 {
		parentRel = qpath.Rel[:slash]
	}
	parentPath := QualifiedPath{RootID: qpath.RootID, Rel: parentRel}
	if nid, ok := c.state.pathIndex[parentPath]; ok {
		if pnode := c.state.nodesByID[nid]; pnode != nil {
			pnode.mtime = time.Now()
			pnode.ctime = pnode.mtime
		}
		for cookie, state := range c.state.dirCookies {
			if state.nodeID == nid {
				delete(c.state.dirCookies, cookie)
			}
		}
	}
}

func ensureCopiedUpNode(c *Core, qpath QualifiedPath) (*privateNode, error) {
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
	if nid, ok := c.state.pathIndex[qpath]; ok {
		if n := c.state.nodesByID[nid]; n != nil && !n.flags.unlinked {
			return n, nil
		}
	}
	switch res.attr.Kind {
	case EntryRegular:
		return ensureWritableNodeLocked(c, qpath, base, OpenWriteOnly, res.attr.Mode)
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

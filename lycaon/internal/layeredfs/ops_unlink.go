package layeredfs

import (
	"errors"
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
	for {
		startGen, err := currentNamespaceGen(c)
		if err != nil {
			return EntryAttr{}, err
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
		if c.state.released {
			c.mu.Unlock()
			return EntryAttr{}, ErrViewReleased
		}
		node, err := ensureWritableNodeLocked(c, oldTarget, oldBase, OpenWriteOnly, oldRes.attr.Mode, startGen)
		c.mu.Unlock()
		if errors.Is(err, errNamespaceRetry) {
			continue
		}
		if err != nil {
			return EntryAttr{}, err
		}

		runNamespacePreCommitHook(c, "link", newTarget)

		c.mu.Lock()
		if c.state.released {
			c.mu.Unlock()
			return EntryAttr{}, ErrViewReleased
		}
		if c.state.nsGen != startGen || c.state.pathIndex[oldTarget] != node.id || node.flags.unlinked ||
			!isParentDirValidLocked(c, oldTarget) || !isParentDirValidLocked(c, newTarget) {
			c.mu.Unlock()
			continue
		}
		if _, exists := c.state.pathIndex[newTarget]; exists {
			c.mu.Unlock()
			return EntryAttr{}, os.ErrExist
		}
		node.paths[newTarget] = struct{}{}
		node.ctime = time.Now()
		node.flags.metadataChanged = true
		delete(c.state.deletedPaths, newTarget)
		c.state.pathIndex[newTarget] = node.id
		bumpParentDirGenLocked(c, newTarget)
		recordJournalLocked(c, JournalOpHardLink, newTarget, &oldTarget, node, 0)
		attr := nodeToAttrLocked(node)
		c.mu.Unlock()
		return attr, nil
	}
}

// Unlink removes a file or symlink (or empty directory when removeDir is true).
// Open handles to an unlinked file remain valid for read/write/stat until closed (#1657).
func (c *Core) Unlink(qpath QualifiedPath, removeDir bool) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()
	return unlinkPathAdmitted(c, qpath, removeDir)
}

func unlinkPathAdmitted(c *Core, qpath QualifiedPath, removeDir bool) error {
	for {
		startGen, err := currentNamespaceGen(c)
		if err != nil {
			return err
		}
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

		runNamespacePreCommitHook(c, "unlink", targetPath)

		c.mu.Lock()
		if c.state.released {
			c.mu.Unlock()
			return ErrViewReleased
		}
		if c.state.nsGen != startGen || !isParentDirValidLocked(c, targetPath) {
			c.mu.Unlock()
			continue
		}
		if removeDir && hasPrivateChildrenLocked(c, targetPath) {
			c.mu.Unlock()
			return syscall.ENOTEMPTY
		}
		if res.privNode != nil && c.state.pathIndex[targetPath] != res.privNode.id {
			c.mu.Unlock()
			continue
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
}

// RemoveAll recursively removes a file or directory subtree, capturing bases only for the affected subtree (#1657).
func (c *Core) RemoveAll(qpath QualifiedPath) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()
	return removeAllPathAdmitted(c, qpath)
}

func removeAllPathAdmitted(c *Core, qpath QualifiedPath) error {
	res, err := resolvePath(c, qpath, false, true)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if res.attr.Kind != EntryDirectory {
		return unlinkPathAdmitted(c, res.qpath, false)
	}
	children, err := readDirUnlocked(c, res.qpath)
	if err != nil {
		return err
	}
	for _, child := range children {
		childPath := QualifiedPath{RootID: res.qpath.RootID, Rel: joinRel(res.qpath.Rel, child.Name)}
		if err := removeAllPathAdmitted(c, childPath); err != nil {
			return err
		}
	}
	return unlinkPathAdmitted(c, res.qpath, true)
}

func bumpParentDirGenLocked(c *Core, qpath QualifiedPath) {
	c.state.nsGen++
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

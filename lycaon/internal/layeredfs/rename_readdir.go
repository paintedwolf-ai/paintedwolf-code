package layeredfs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Rename atomically moves oldPath to newPath within the same workspace root.
// If newPath already exists, its base is captured before replacement even when oldPath is a privately created temp file (#1657).
func (c *Core) Rename(oldPath, newPath QualifiedPath) error {
	return renamePath(c, oldPath, newPath)
}

func renamePath(c *Core, oldPath, newPath QualifiedPath) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()

	if oldPath.RootID != newPath.RootID {
		return syscall.EXDEV
	}
	for {
		startGen, err := currentNamespaceGen(c)
		if err != nil {
			return err
		}
		oldParent, oldName, err := resolveParentDir(c, oldPath, true)
		if err != nil {
			return err
		}
		srcPath := QualifiedPath{RootID: oldParent.RootID, Rel: joinRel(oldParent.Rel, oldName)}
		srcPath, err = checkGitAccess(c, srcPath, true)
		if err != nil {
			return err
		}

		newParent, newName, err := resolveParentDir(c, newPath, true)
		if err != nil {
			return err
		}
		dstPath := QualifiedPath{RootID: newParent.RootID, Rel: joinRel(newParent.Rel, newName)}
		dstPath, err = checkGitAccess(c, dstPath, true)
		if err != nil {
			return err
		}

		if srcPath == dstPath {
			_, err := lookupExactComponent(c, srcPath)
			return err
		}
		if strings.HasPrefix(dstPath.Rel, srcPath.Rel+"/") {
			return syscall.EINVAL
		}

		srcRes, err := lookupExactComponent(c, srcPath)
		if err != nil {
			return err
		}
		dstRes, dstErr := lookupExactComponent(c, dstPath)
		if dstErr != nil && !os.IsNotExist(dstErr) {
			return dstErr
		}
		dstExists := dstErr == nil
		if dstExists {
			if srcRes.privNode != nil && dstRes.privNode != nil && srcRes.privNode.id == dstRes.privNode.id {
				return nil
			}
			if srcRes.attr.Kind == EntryDirectory && dstRes.attr.Kind != EntryDirectory {
				return syscall.ENOTDIR
			}
			if srcRes.attr.Kind != EntryDirectory && dstRes.attr.Kind == EntryDirectory {
				return syscall.EISDIR
			}
			if dstRes.attr.Kind == EntryDirectory {
				children, cErr := readDirUnlocked(c, dstPath)
				if cErr != nil {
					return cErr
				}
				if len(children) > 0 {
					return syscall.ENOTEMPTY
				}
			}
		}

		if _, err := ensureBaseCaptured(c, srcPath); err != nil {
			return err
		}
		dstBase, err := ensureBaseCaptured(c, dstPath)
		if err != nil {
			return err
		}

		if srcRes.attr.Kind == EntryDirectory {
			err := renameDirectorySubtree(c, srcPath, dstPath, dstExists, startGen)
			if errors.Is(err, errNamespaceRetry) {
				continue
			}
			return err
		}

		srcNode, err := ensureCopiedUpNode(c, srcPath, startGen)
		if errors.Is(err, errNamespaceRetry) {
			continue
		}
		if err != nil {
			return err
		}

		err = commitFileRename(c, srcPath, dstPath, srcNode, srcRes.attr.Kind, dstExists, dstBase.Kind, startGen)
		if errors.Is(err, errNamespaceRetry) {
			continue
		}
		return err
	}
}

// commitFileRename publishes a non-directory rename, returning errNamespaceRetry if the namespace
// moved since the caller resolved its paths.
func commitFileRename(c *Core, srcPath, dstPath QualifiedPath, srcNode *privateNode, srcKind EntryKind, dstExists bool, dstBaseKind EntryKind, startGen uint64) error {
	runNamespacePreCommitHook(c, "rename", srcPath)

	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return ErrViewReleased
	}
	if c.state.nsGen != startGen || c.state.pathIndex[srcPath] != srcNode.id || srcNode.flags.unlinked ||
		!isParentDirValidLocked(c, srcPath) || !isParentDirValidLocked(c, dstPath) {
		c.mu.Unlock()
		return errNamespaceRetry
	}

	var replacedNodeID NodeID
	var replacedNode *privateNode
	var orphanBacking string
	if existingID, ok := c.state.pathIndex[dstPath]; ok {
		if existingID == srcNode.id {
			c.mu.Unlock()
			return nil
		}
		replacedNodeID = existingID
		if existingNode := c.state.nodesByID[existingID]; existingNode != nil {
			if existingNode.kind == EntryDirectory {
				c.mu.Unlock()
				return syscall.EISDIR
			}
			replacedNode = existingNode
			delete(existingNode.paths, dstPath)
			existingNode.ctime = time.Now()
			if len(existingNode.paths) == 0 {
				existingNode.flags.unlinked = true
				if !hasOpenHandlesForNodeLocked(c, existingNode.id) {
					orphanBacking = existingNode.flags.backingName
					reclaimNodeLocked(c, existingNode)
				}
			}
		}
	}

	delete(c.state.transient, srcPath)
	delete(c.state.transient, dstPath)
	delete(c.state.pathIndex, srcPath)
	delete(srcNode.paths, srcPath)

	srcHost, srcRepoRoot, srcIsGit, srcGitErr := resolveMutableGitTarget(c, srcPath)
	if srcGitErr != nil {
		c.mu.Unlock()
		return srcGitErr
	}
	dstHost, dstRepoRoot, dstIsGit, dstGitErr := resolveMutableGitTarget(c, dstPath)
	if dstGitErr != nil {
		c.mu.Unlock()
		return dstGitErr
	}
	if srcIsGit && dstIsGit && !srcRepoRoot && !dstRepoRoot {
		if err := os.MkdirAll(filepath.Dir(dstHost), 0o755); err != nil {
			c.mu.Unlock()
			return err
		}
		if err := os.Rename(srcHost, dstHost); err != nil {
			c.mu.Unlock()
			return err
		}
		srcNode.flags.gitHostPath = dstHost
		if srcNode.kind == EntryRegular {
			srcNode.flags.backingPath = dstHost
			markDirtyBackingFileLocked(c, dstHost)
		}
		delete(c.state.deletedPaths, srcPath)
	} else {
		c.state.deletedPaths[srcPath] = deletedState{
			ReplacedKind: srcKind,
			Timestamp:    time.Now(),
		}
	}

	_, wasDeletedDir := c.state.deletedPaths[dstPath]
	delete(c.state.deletedPaths, dstPath)
	srcNode.paths[dstPath] = struct{}{}
	srcNode.ctime = time.Now()
	srcNode.flags.namespaceAdded = true
	c.state.pathIndex[dstPath] = srcNode.id
	updateOpenHandlesPathLocked(c, srcNode.id, srcPath, dstPath)

	bumpParentDirGenLocked(c, srcPath)
	bumpParentDirGenLocked(c, dstPath)

	opKind := JournalOpRename
	if dstExists || replacedNodeID != 0 {
		opKind = JournalOpRenameOver
	}
	if wasDeletedDir || dstBaseKind == EntryDirectory {
		opKind = JournalOpReplaceDir
	}
	recordJournalLocked(c, opKind, dstPath, &srcPath, srcNode, 0)
	var workDirFD = -1
	if orphanBacking != "" && c.backing.workDirFile != nil {
		workDirFD = int(c.backing.workDirFile.Fd())
	}
	c.mu.Unlock()
	if orphanBacking != "" && workDirFD >= 0 && replacedNode != nil {
		replacedNode.flags.ioMu.Lock()
		_ = unlinkBackingFile(workDirFD, orphanBacking)
		replacedNode.flags.ioMu.Unlock()
	}
	return nil
}

func renameDirectorySubtree(c *Core, srcPath, dstPath QualifiedPath, dstExists bool, startGen uint64) error {
	subtreePaths, err := collectDirectorySubtreePaths(c, srcPath)
	if err != nil {
		return err
	}
	srcPrefix := srcPath.Rel + "/"
	for _, p := range subtreePaths {
		if _, err := ensureCopiedUpNode(c, p, startGen); err != nil {
			return err
		}
		if strings.HasPrefix(p.Rel, srcPrefix) {
			suffix := strings.TrimPrefix(p.Rel, srcPrefix)
			dstChild := QualifiedPath{RootID: dstPath.RootID, Rel: joinRel(dstPath.Rel, suffix)}
			if _, err := ensureBaseCaptured(c, dstChild); err != nil {
				return err
			}
		}
	}

	runNamespacePreCommitHook(c, "rename", srcPath)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	if c.state.nsGen != startGen || !isParentDirValidLocked(c, srcPath) || !isParentDirValidLocked(c, dstPath) {
		return errNamespaceRetry
	}
	if hasPrivateChildrenLocked(c, dstPath) {
		return syscall.ENOTEMPTY
	}
	srcID, ok := c.state.pathIndex[srcPath]
	if !ok || c.state.nodesByID[srcID] == nil || c.state.nodesByID[srcID].flags.unlinked || c.state.nodesByID[srcID].kind != EntryDirectory {
		return errNamespaceRetry
	}

	var affected []QualifiedPath
	for qp := range c.state.pathIndex {
		if qp.RootID == srcPath.RootID && (qp.Rel == srcPath.Rel || strings.HasPrefix(qp.Rel, srcPrefix)) {
			affected = append(affected, qp)
		}
	}
	sort.Slice(affected, func(i, j int) bool {
		return len(affected[i].Rel) < len(affected[j].Rel)
	})

	srcHost, srcRepoRoot, srcIsGit, srcGitErr := resolveMutableGitTarget(c, srcPath)
	if srcGitErr != nil {
		return srcGitErr
	}
	dstHost, dstRepoRoot, dstIsGit, dstGitErr := resolveMutableGitTarget(c, dstPath)
	if dstGitErr != nil {
		return dstGitErr
	}
	isGitDirRename := srcIsGit && dstIsGit && !srcRepoRoot && !dstRepoRoot
	if isGitDirRename {
		if err := os.MkdirAll(filepath.Dir(dstHost), 0o755); err != nil {
			return err
		}
		if err := os.Rename(srcHost, dstHost); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	now := time.Now()
	for _, oldQP := range affected {
		nid := c.state.pathIndex[oldQP]
		node := c.state.nodesByID[nid]
		delete(c.state.pathIndex, oldQP)
		delete(c.state.transient, oldQP)
		if isGitDirRename {
			delete(c.state.deletedPaths, oldQP)
		} else {
			c.state.deletedPaths[oldQP] = deletedState{
				ReplacedKind: node.kind,
				Timestamp:    now,
			}
		}
		var newRel string
		if oldQP.Rel == srcPath.Rel {
			newRel = dstPath.Rel
		} else {
			suffix := strings.TrimPrefix(oldQP.Rel, srcPrefix)
			newRel = joinRel(dstPath.Rel, suffix)
		}
		newQP := QualifiedPath{RootID: dstPath.RootID, Rel: newRel}
		if existingID, exists := c.state.pathIndex[newQP]; exists && existingID != nid {
			if existingNode := c.state.nodesByID[existingID]; existingNode != nil {
				delete(existingNode.paths, newQP)
				existingNode.ctime = now
				if len(existingNode.paths) == 0 {
					existingNode.flags.unlinked = true
					if !hasOpenHandlesForNodeLocked(c, existingNode.id) {
						reclaimNodeLocked(c, existingNode)
					}
				}
			}
		}
		delete(c.state.deletedPaths, newQP)
		delete(node.paths, oldQP)
		node.paths[newQP] = struct{}{}
		node.ctime = now
		if isGitDirRename {
			if newHost, _, ok, _ := resolveMutableGitTarget(c, newQP); ok {
				node.flags.gitHostPath = newHost
				if node.kind == EntryRegular {
					node.flags.backingPath = newHost
					markDirtyBackingFileLocked(c, newHost)
				}
			}
			if node.kind == EntryDirectory {
				node.flags.namespaceAdded = false
			} else {
				node.flags.namespaceAdded = true
			}
		} else {
			node.flags.namespaceAdded = true
		}
		c.state.pathIndex[newQP] = nid
		updateOpenHandlesPathLocked(c, nid, oldQP, newQP)
	}

	bumpParentDirGenLocked(c, srcPath)
	bumpParentDirGenLocked(c, dstPath)

	rootNode := c.state.nodesByID[c.state.pathIndex[dstPath]]
	opKind := JournalOpRename
	if dstExists {
		opKind = JournalOpRenameOver
	}
	recordJournalLocked(c, opKind, dstPath, &srcPath, rootNode, 0)
	return nil
}

func collectDirectorySubtreePaths(c *Core, dirPath QualifiedPath) ([]QualifiedPath, error) {
	out := []QualifiedPath{dirPath}
	entries, err := readDirUnlocked(c, dirPath)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		childPath := QualifiedPath{RootID: dirPath.RootID, Rel: joinRel(dirPath.Rel, e.Name)}
		if e.Kind == EntryDirectory {
			sub, sErr := collectDirectorySubtreePaths(c, childPath)
			if sErr != nil {
				return nil, sErr
			}
			out = append(out, sub...)
		} else {
			out = append(out, childPath)
		}
	}
	return out, nil
}

func updateOpenHandlesPathLocked(c *Core, nid NodeID, oldQP, newQP QualifiedPath) {
	for _, h := range c.state.handles {
		if !h.closed && h.nodeID == nid && h.qpath == oldQP {
			h.qpath = newQP
		}
	}
}

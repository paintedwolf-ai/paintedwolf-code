package layeredfs

import (
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
)

const maxSymlinkHops = 16

type resolvedLayerEntry struct {
	qpath       QualifiedPath
	layer       LayerSource
	attr        EntryAttr
	privNode    *privateNode
	parentEntry *ParentEntry
	lowerEntry  *LowerEntry
}

// Stat resolves a path following symlinks within the root boundary and returns its attributes.
func (c *Core) Stat(qpath QualifiedPath) (EntryAttr, error) {
	res, err := resolvePath(c, qpath, true, false)
	if err != nil {
		return EntryAttr{}, err
	}
	emitObservation(c, ObservationEvent{
		Path:       res.qpath,
		Layer:      res.layer,
		NodeID:     res.attr.NodeID,
		Generation: res.attr.Generation,
		Kind:       ObserveStat,
		Size:       res.attr.Size,
		ModTimeNS:  res.attr.ModTime.UnixNano(),
	})
	return res.attr, nil
}

// Lstat resolves a path without following the final component if it is a symlink.
func (c *Core) Lstat(qpath QualifiedPath) (EntryAttr, error) {
	res, err := resolvePath(c, qpath, false, false)
	if err != nil {
		return EntryAttr{}, err
	}
	emitObservation(c, ObservationEvent{
		Path:       res.qpath,
		Layer:      res.layer,
		NodeID:     res.attr.NodeID,
		Generation: res.attr.Generation,
		Kind:       ObserveLookup,
		Size:       res.attr.Size,
		ModTimeNS:  res.attr.ModTime.UnixNano(),
	})
	return res.attr, nil
}

// Readlink reads the target of a symbolic link without following the final component.
func (c *Core) Readlink(qpath QualifiedPath) (string, error) {
	res, err := resolvePath(c, qpath, false, false)
	if err != nil {
		return "", err
	}
	if res.attr.Kind != EntrySymlink {
		return "", syscall.EINVAL
	}
	emitObservation(c, ObservationEvent{
		Path:       res.qpath,
		Layer:      res.layer,
		NodeID:     res.attr.NodeID,
		Generation: res.attr.Generation,
		Kind:       ObserveReadlink,
	})
	return res.attr.LinkTarget, nil
}

func resolvePath(c *Core, qpath QualifiedPath, followFinal bool, writeIntent bool) (resolvedLayerEntry, error) {
	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return resolvedLayerEntry{}, ErrViewReleased
	}
	_, rootOK := c.roots.byID[qpath.RootID]
	c.mu.RUnlock()
	if !rootOK {
		return resolvedLayerEntry{}, ErrInvalidRoot
	}
	rel, err := sanitizeRelPath(qpath.Rel)
	if err != nil {
		return resolvedLayerEntry{}, err
	}
	qpath.Rel = rel
	qpath, err = checkGitAccess(c, qpath, writeIntent)
	if err != nil {
		return resolvedLayerEntry{}, err
	}
	if qpath.Rel == "." {
		return lookupExactComponent(c, qpath)
	}

	parts := strings.Split(qpath.Rel, "/")
	var resolvedParts []string
	hops := 0

	for len(parts) > 0 {
		comp := parts[0]
		parts = parts[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			if len(resolvedParts) == 0 {
				return resolvedLayerEntry{}, ErrSymlinkEscape
			}
			resolvedParts = resolvedParts[:len(resolvedParts)-1]
			continue
		}
		candidateRel := comp
		if len(resolvedParts) > 0 {
			candidateRel = strings.Join(resolvedParts, "/") + "/" + comp
		}
		candPath := QualifiedPath{RootID: qpath.RootID, Rel: candidateRel}
		candPath, err = checkGitAccess(c, candPath, writeIntent && len(parts) == 0)
		if err != nil {
			return resolvedLayerEntry{}, err
		}
		entry, lErr := lookupExactComponent(c, candPath)
		if lErr != nil {
			return resolvedLayerEntry{}, lErr
		}
		isFinal := len(parts) == 0
		if entry.attr.Kind == EntrySymlink && (!isFinal || followFinal) {
			hops++
			if hops > maxSymlinkHops {
				return resolvedLayerEntry{}, syscall.ELOOP
			}
			newParts, reset, sErr := expandSymlinkTarget(c, qpath.RootID, entry.attr.LinkTarget, parts)
			if sErr != nil {
				return resolvedLayerEntry{}, sErr
			}
			if reset {
				resolvedParts = nil
			}
			parts = newParts
			continue
		}
		if !isFinal && entry.attr.Kind != EntryDirectory {
			return resolvedLayerEntry{}, syscall.ENOTDIR
		}
		if isFinal {
			return entry, nil
		}
		resolvedParts = append(resolvedParts, comp)
	}

	finalRel := "."
	if len(resolvedParts) > 0 {
		finalRel = strings.Join(resolvedParts, "/")
	}
	return lookupExactComponent(c, QualifiedPath{RootID: qpath.RootID, Rel: finalRel})
}

func expandSymlinkTarget(c *Core, rootID, rawTarget string, remaining []string) ([]string, bool, error) {
	target := filepath.ToSlash(rawTarget)
	if !strings.HasPrefix(target, "/") {
		return append(strings.Split(target, "/"), remaining...), false, nil
	}
	rootSpec := c.roots.byID[rootID]
	if rootSpec.TrunkPath == "" {
		return nil, false, ErrSymlinkEscape
	}
	rootClean := filepath.ToSlash(filepath.Clean(rootSpec.TrunkPath))
	targetClean := filepath.ToSlash(filepath.Clean(target))
	if targetClean != rootClean && !strings.HasPrefix(targetClean, rootClean+"/") {
		return nil, false, ErrSymlinkEscape
	}
	relTarget := strings.TrimPrefix(strings.TrimPrefix(targetClean, rootClean), "/")
	if relTarget == "" {
		return remaining, true, nil
	}
	return append(strings.Split(relTarget, "/"), remaining...), true, nil
}

func resolveParentDir(c *Core, qpath QualifiedPath, writeIntent bool) (QualifiedPath, string, error) {
	rel, err := sanitizeRelPath(qpath.Rel)
	if err != nil {
		return QualifiedPath{}, "", err
	}
	if rel == "." {
		return QualifiedPath{}, "", syscall.EINVAL
	}
	qpath.Rel = rel
	qpath, err = checkGitAccess(c, qpath, writeIntent)
	if err != nil {
		return QualifiedPath{}, "", err
	}
	slash := strings.LastIndexByte(qpath.Rel, '/')
	parentRel, name := ".", qpath.Rel
	if slash >= 0 {
		parentRel, name = qpath.Rel[:slash], qpath.Rel[slash+1:]
	}
	if name == "" || name == "." || name == ".." {
		return QualifiedPath{}, "", ErrPathEscape
	}
	parentRes, err := resolvePath(c, QualifiedPath{RootID: qpath.RootID, Rel: parentRel}, true, writeIntent)
	if err != nil {
		return QualifiedPath{}, "", err
	}
	if parentRes.attr.Kind != EntryDirectory {
		return QualifiedPath{}, "", syscall.ENOTDIR
	}
	return parentRes.qpath, name, nil
}

func lookupExactComponent(c *Core, qpath QualifiedPath) (resolvedLayerEntry, error) {
	qpath.Rel = cleanRelPath(qpath.Rel)

	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return resolvedLayerEntry{}, ErrViewReleased
	}
	if nodeID, ok := c.state.pathIndex[qpath]; ok {
		node := c.state.nodesByID[nodeID]
		if node != nil && !node.flags.unlinked {
			attr := nodeToAttrLocked(node)
			c.mu.RUnlock()
			return resolvedLayerEntry{
				qpath:    qpath,
				layer:    LayerPrivate,
				attr:     attr,
				privNode: node,
			}, nil
		}
	}
	if isMaskedByPrivateDeletionOrReplacementLocked(c, qpath) {
		c.mu.RUnlock()
		return resolvedLayerEntry{}, fs.ErrNotExist
	}
	parentLayers := c.cfg.ParentLayers
	provider := c.roots.providers[qpath.RootID]
	c.mu.RUnlock()

	for _, pl := range parentLayers {
		pe, ok, err := pl.Lookup(qpath)
		if err != nil {
			return resolvedLayerEntry{}, err
		}
		if !ok {
			continue
		}
		if pe.Kind == EntryAbsent {
			return resolvedLayerEntry{}, fs.ErrNotExist
		}
		cp := pe
		return resolvedLayerEntry{
			qpath:       qpath,
			layer:       LayerParent,
			attr:        parentEntryToAttr(qpath, pe),
			parentEntry: &cp,
		}, nil
	}

	if provider == nil {
		if qpath.Rel == "." {
			return resolvedLayerEntry{
				qpath: qpath,
				layer: LayerTrunk,
				attr: EntryAttr{
					NodeID:     syntheticNodeID(qpath, 1),
					Generation: 1,
					Layer:      LayerTrunk,
					Kind:       EntryDirectory,
					Mode:       fs.ModeDir | 0o755,
					Nlink:      2,
				},
			}, nil
		}
		return resolvedLayerEntry{}, fs.ErrNotExist
	}

	c.counters.lowerLookups.Add(1)
	le, err := provider.Lstat(qpath.Rel)
	if err != nil {
		return resolvedLayerEntry{}, err
	}
	if le.Kind == EntryAbsent {
		return resolvedLayerEntry{}, fs.ErrNotExist
	}
	xattrs, xErr := provider.ListXattr(qpath.Rel)
	if xErr != nil {
		return resolvedLayerEntry{}, xErr
	}
	return resolvedLayerEntry{
		qpath:      qpath,
		layer:      LayerTrunk,
		attr:       lowerEntryToAttr(qpath, le, xattrs),
		lowerEntry: &le,
	}, nil
}

func isMaskedByPrivateDeletionOrReplacementLocked(c *Core, qpath QualifiedPath) bool {
	if _, deleted := c.state.deletedPaths[qpath]; deleted {
		return true
	}
	rel := qpath.Rel
	for rel != "." {
		if _, del := c.state.deletedPaths[QualifiedPath{RootID: qpath.RootID, Rel: rel}]; del {
			return true
		}
		if ancID, ok := c.state.pathIndex[QualifiedPath{RootID: qpath.RootID, Rel: rel}]; ok {
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

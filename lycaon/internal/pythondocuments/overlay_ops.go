package pythondocuments

import (
	"context"
	"io/fs"
	"path"
	"sort"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func (a *OverlayAdapter) Open(ctx context.Context, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	target, rootSpec, errno := a.resolvePath(req.Path)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	if target.Namespace == pythonruntime.NamespaceNullDevice {
		return &nullGuestFile{target: target, now: a.now}, pythonruntime.ErrnoSuccess
	}
	isWrite := req.Flags&(pythonruntime.OpenWrite|pythonruntime.OpenCreate|pythonruntime.OpenTruncate|pythonruntime.OpenAppend) != 0
	if rootSpec.ReadOnly && isWrite {
		return nil, pythonruntime.ErrnoEROFS
	}
	if target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		data, ok := a.roFiles[CanonicalKeyOf(target)]
		if !ok {
			return nil, pythonruntime.ErrnoENOENT
		}
		node := &overlayNode{
			kind:    nodeFile,
			target:  target,
			data:    append([]byte(nil), data...),
			mode:    0o444,
			modTime: a.now().UTC(),
		}
		return &overlayFile{adapter: a, node: node, flags: req.Flags, parentOpID: ParentOperationID(ctx), writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	followFinal := req.Flags&pythonruntime.OpenNoFollow == 0
	resolved, symErrno := a.followSymlinksLocked(ctx, target, followFinal)
	if symErrno != pythonruntime.ErrnoSuccess {
		return nil, symErrno
	}
	target = resolved
	parentOpID := ParentOperationID(ctx)

	if target.Namespace == pythonruntime.NamespaceScratch {
		return a.openScratchLocked(ctx, parentOpID, target, req)
	}
	return a.openWorkspaceLocked(ctx, parentOpID, target, req, isWrite)
}

func (a *OverlayAdapter) openScratchLocked(ctx context.Context, parentOpID string, target pythonruntime.CanonicalTarget, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	n, exists := a.scratch[k]
	if exists && n.kind == nodeTombstone {
		exists = false
		n = nil
	}
	isWrite := req.Flags&(pythonruntime.OpenWrite|pythonruntime.OpenCreate|pythonruntime.OpenTruncate|pythonruntime.OpenAppend) != 0
	if exists {
		if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
			return nil, pythonruntime.ErrnoEEXIST
		}
		if n.kind == nodeDir {
			if isWrite {
				return nil, pythonruntime.ErrnoEISDIR
			}
			return &overlayFile{adapter: a, node: n, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
		}
		if req.Flags&pythonruntime.OpenDirectory != 0 {
			return nil, pythonruntime.ErrnoENOTDIR
		}
		if req.Flags&pythonruntime.OpenTruncate != 0 {
			n.mu.Lock()
			n.data = nil
			n.modTime = a.now().UTC()
			n.mu.Unlock()
		}
		return &overlayFile{adapter: a, node: n, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}
	if req.Flags&pythonruntime.OpenCreate == 0 {
		return nil, pythonruntime.ErrnoENOENT
	}
	if errParent := a.ensureParentDirLocked(ctx, target); errParent != pythonruntime.ErrnoSuccess {
		return nil, errParent
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	n = &overlayNode{
		kind:             nodeFile,
		target:           target,
		mode:             mode,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		parentOpID:       parentOpID,
		writeOriginFrame: req.OriginFrame,
	}
	a.scratch[k] = n
	return &overlayFile{adapter: a, node: n, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) openWorkspaceLocked(ctx context.Context, parentOpID string, target pythonruntime.CanonicalTarget, req pythonruntime.OpenRequest, isWrite bool) (pythonruntime.GuestFile, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	if ov, inOverlay := a.overlay[k]; inOverlay && ov.kind != nodeTombstone {
		return a.openExistingOverlayNodeLocked(parentOpID, ov, req, isWrite)
	}

	hostExists := false
	var snap DocumentSnapshot
	if _, isTombstone := a.overlay[k]; !isTombstone {
		origin := PinOriginRead
		if isWrite && req.Flags&pythonruntime.OpenRead == 0 {
			origin = PinOriginBlindWrite
		}
		var snapErrno pythonruntime.Errno
		snap, snapErrno = a.pinHostStateLocked(ctx, target, origin)
		if snapErrno == pythonruntime.ErrnoSuccess {
			hostExists = true
		} else if snapErrno != pythonruntime.ErrnoENOENT {
			return nil, snapErrno
		}
	}

	if hostExists {
		return a.openHostSnapshotLocked(parentOpID, k, snap, req, isWrite)
	}
	return a.createOverlayFileLocked(ctx, parentOpID, k, target, req)
}

func (a *OverlayAdapter) openExistingOverlayNodeLocked(parentOpID string, ov *overlayNode, req pythonruntime.OpenRequest, isWrite bool) (pythonruntime.GuestFile, pythonruntime.Errno) {
	if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
		return nil, pythonruntime.ErrnoEEXIST
	}
	if ov.kind == nodeDir {
		if isWrite {
			return nil, pythonruntime.ErrnoEISDIR
		}
		return &overlayFile{adapter: a, node: ov, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}
	if req.Flags&pythonruntime.OpenDirectory != 0 {
		return nil, pythonruntime.ErrnoENOTDIR
	}
	if req.Flags&pythonruntime.OpenTruncate != 0 {
		ov.mu.Lock()
		ov.data = nil
		ov.contentModified = true
		ov.modTime = a.now().UTC()
		if ov.writeOriginFrame == nil {
			ov.writeOriginFrame = req.OriginFrame
		}
		if ov.parentOpID == "" {
			ov.parentOpID = parentOpID
		}
		ov.mu.Unlock()
		ov.mutationSeq = a.allocSeqLocked()
		a.recordOverlayEventLocked(parentOpID, "truncate", ov.target, nil, 0, req.OriginFrame, nil)
	}
	return &overlayFile{adapter: a, node: ov, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) openHostSnapshotLocked(parentOpID string, k CanonicalKey, snap DocumentSnapshot, req pythonruntime.OpenRequest, isWrite bool) (pythonruntime.GuestFile, pythonruntime.Errno) {
	if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
		return nil, pythonruntime.ErrnoEEXIST
	}
	if snap.IsDir {
		if isWrite {
			return nil, pythonruntime.ErrnoEISDIR
		}
		dirNode := &overlayNode{
			kind:    nodeDir,
			target:  snap.Target,
			mode:    snap.Mode,
			modTime: snap.ModTime,
			inode:   snap.Inode,
		}
		return &overlayFile{adapter: a, node: dirNode, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}
	if req.Flags&pythonruntime.OpenDirectory != 0 {
		return nil, pythonruntime.ErrnoENOTDIR
	}
	var readOpID string
	if req.Flags&pythonruntime.OpenRead != 0 || !isWrite {
		readOpID = a.recordDocumentReadWitnessLocked(parentOpID, snap, req.OriginFrame)
	}
	data := append([]byte(nil), snap.Bytes...)
	if !isWrite {
		roNode := &overlayNode{
			kind:    nodeFile,
			target:  snap.Target,
			data:    data,
			mode:    snap.Mode,
			modTime: snap.ModTime,
			inode:   snap.Inode,
		}
		return &overlayFile{adapter: a, node: roNode, flags: req.Flags, readOpID: readOpID, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}
	if req.Flags&pythonruntime.OpenTruncate != 0 {
		data = nil
	}
	node := &overlayNode{
		kind:             nodeFile,
		target:           snap.Target,
		data:             data,
		mode:             snap.Mode,
		modTime:          a.now().UTC(),
		inode:            snap.Inode,
		createdInOverlay: false,
		contentModified:  req.Flags&pythonruntime.OpenTruncate != 0,
		mutationSeq:      a.allocSeqLocked(),
		parentOpID:       parentOpID,
		writeOriginFrame: req.OriginFrame,
	}
	a.overlay[k] = node
	if req.Flags&pythonruntime.OpenTruncate != 0 {
		a.recordOverlayEventLocked(parentOpID, "truncate", node.target, nil, 0, req.OriginFrame, nil)
	} else {
		a.recordOverlayEventLocked(parentOpID, "open", node.target, nil, int64(len(data)), req.OriginFrame, nil)
	}
	return &overlayFile{adapter: a, node: node, flags: req.Flags, readOpID: readOpID, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) createOverlayFileLocked(ctx context.Context, parentOpID string, k CanonicalKey, target pythonruntime.CanonicalTarget, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	if req.Flags&pythonruntime.OpenCreate == 0 {
		return nil, pythonruntime.ErrnoENOENT
	}
	if errParent := a.ensureParentDirLocked(ctx, target); errParent != pythonruntime.ErrnoSuccess {
		return nil, errParent
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	node := &overlayNode{
		kind:             nodeFile,
		target:           target,
		mode:             mode,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		contentModified:  true,
		mutationSeq:      a.allocSeqLocked(),
		parentOpID:       parentOpID,
		writeOriginFrame: req.OriginFrame,
	}
	a.overlay[k] = node
	a.recordOverlayEventLocked(parentOpID, "open", target, nil, 0, req.OriginFrame, nil)
	return &overlayFile{adapter: a, node: node, flags: req.Flags, parentOpID: parentOpID, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Stat(ctx context.Context, rawPath string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	return a.statImpl(ctx, rawPath, true)
}

func (a *OverlayAdapter) Lstat(ctx context.Context, rawPath string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	return a.statImpl(ctx, rawPath, false)
}

func (a *OverlayAdapter) statImpl(ctx context.Context, rawPath string, follow bool) (pythonruntime.StatInfo, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return pythonruntime.StatInfo{}, errno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, follow)
	if symErrno != pythonruntime.ErrnoSuccess {
		return pythonruntime.StatInfo{}, symErrno
	}
	return a.statTargetLocked(ctx, resolved)
}

func (a *OverlayAdapter) ReadDir(ctx context.Context, rawPath string) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, true)
	if symErrno != pythonruntime.ErrnoSuccess {
		return nil, symErrno
	}
	return a.readDirLocked(ctx, resolved)
}

func (a *OverlayAdapter) readDirLocked(ctx context.Context, target pythonruntime.CanonicalTarget) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	info, errno := a.statTargetLocked(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	if !info.IsDir {
		return nil, pythonruntime.ErrnoENOTDIR
	}
	merged := make(map[string]pythonruntime.DirEntry)
	dirRel := cleanRelPath(target.RelPath)

	if target.Namespace == pythonruntime.NamespaceWorkspace && a.store != nil {
		if hostEntries, hostErrno := a.store.ListDir(ctx, target); hostErrno == pythonruntime.ErrnoSuccess {
			for _, e := range hostEntries {
				merged[e.Name] = e
			}
		}
	}

	var layer map[CanonicalKey]*overlayNode
	if target.Namespace == pythonruntime.NamespaceScratch {
		layer = a.scratch
	} else if target.Namespace == pythonruntime.NamespaceWorkspace {
		layer = a.overlay
	}
	for k, n := range layer {
		if k.Namespace != target.Namespace || k.RootID != target.RootID || k.Branch != target.Branch || k.RelPath == "" || k.RelPath == dirRel {
			continue
		}
		parent := path.Dir(k.RelPath)
		if parent == "." {
			parent = ""
		}
		if parent != dirRel {
			continue
		}
		name := path.Base(k.RelPath)
		if n.kind == nodeTombstone {
			delete(merged, name)
			continue
		}
		merged[name] = pythonruntime.DirEntry{
			Name:      name,
			IsDir:     n.kind == nodeDir,
			IsSymlink: n.kind == nodeSymlink,
			Mode:      n.mode,
		}
	}

	out := make([]pythonruntime.DirEntry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Mkdir(ctx context.Context, rawPath string, perm fs.FileMode) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, stErrno := a.statTargetLocked(ctx, target); stErrno == pythonruntime.ErrnoSuccess {
		return pythonruntime.ErrnoEEXIST
	}
	if parentErrno := a.ensureParentDirLocked(ctx, target); parentErrno != pythonruntime.ErrnoSuccess {
		return parentErrno
	}
	if perm == 0 {
		perm = 0o755
	}
	parentOpID := ParentOperationID(ctx)
	node := &overlayNode{
		kind:             nodeDir,
		target:           target,
		mode:             perm | fs.ModeDir,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		mutationSeq:      a.allocSeqLocked(),
		parentOpID:       parentOpID,
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		a.scratch[k] = node
	} else {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = node
		a.recordOverlayEventLocked(parentOpID, "mkdir", target, nil, 0, nil, nil)
	}
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Unlink(ctx context.Context, rawPath string) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		return symErrno
	}
	target = resolved
	info, stErrno := a.statTargetLocked(ctx, target)
	if stErrno != pythonruntime.ErrnoSuccess {
		return stErrno
	}
	if info.IsDir {
		return pythonruntime.ErrnoEISDIR
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		delete(a.scratch, k)
		return pythonruntime.ErrnoSuccess
	}
	parentOpID := ParentOperationID(ctx)
	if a.existedInHostLocked(ctx, target) {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = &overlayNode{
			kind:        nodeTombstone,
			target:      target,
			mutationSeq: a.allocSeqLocked(),
			parentOpID:  parentOpID,
		}
	} else {
		delete(a.overlay, k)
	}
	a.recordOverlayEventLocked(parentOpID, "unlink", target, nil, 0, nil, nil)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Rmdir(ctx context.Context, rawPath string) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	info, stErrno := a.statTargetLocked(ctx, target)
	if stErrno != pythonruntime.ErrnoSuccess {
		return stErrno
	}
	if !info.IsDir {
		return pythonruntime.ErrnoENOTDIR
	}
	entries, rdErrno := a.readDirLocked(ctx, target)
	if rdErrno != pythonruntime.ErrnoSuccess {
		return rdErrno
	}
	if len(entries) > 0 {
		return pythonruntime.ErrnoEEXIST
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		delete(a.scratch, k)
		return pythonruntime.ErrnoSuccess
	}
	parentOpID := ParentOperationID(ctx)
	if a.existedInHostLocked(ctx, target) {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = &overlayNode{
			kind:        nodeTombstone,
			target:      target,
			mode:        fs.ModeDir,
			mutationSeq: a.allocSeqLocked(),
			parentOpID:  parentOpID,
		}
	} else {
		delete(a.overlay, k)
	}
	a.recordOverlayEventLocked(parentOpID, "rmdir", target, nil, 0, nil, nil)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Rename(ctx context.Context, oldPath, newPath string) pythonruntime.Errno {
	oldTarget, oldRoot, errno := a.resolvePath(oldPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	newTarget, newRoot, errno := a.resolvePath(newPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if oldTarget.Namespace != newTarget.Namespace || oldTarget.RootID != newTarget.RootID || oldTarget.Branch != newTarget.Branch {
		return pythonruntime.ErrnoEXDEV
	}
	if oldRoot.ReadOnly || newRoot.ReadOnly || oldTarget.Namespace == pythonruntime.NamespaceRuntime || oldTarget.Namespace == pythonruntime.NamespacePackages {
		return pythonruntime.ErrnoEROFS
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var symErrno pythonruntime.Errno
	oldTarget, symErrno = a.followSymlinksLocked(ctx, oldTarget, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		return symErrno
	}
	newTarget, symErrno = a.followSymlinksLocked(ctx, newTarget, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		return symErrno
	}
	if errParent := a.ensureParentDirLocked(ctx, newTarget); errParent != pythonruntime.ErrnoSuccess {
		return errParent
	}

	oldKey := CanonicalKeyOf(oldTarget)
	newKey := CanonicalKeyOf(newTarget)
	if oldTarget.Namespace == pythonruntime.NamespaceScratch {
		return a.renameScratchLocked(oldKey, newKey, newTarget)
	}
	return a.renameWorkspaceLocked(ctx, ParentOperationID(ctx), oldKey, newKey, oldTarget, newTarget)
}

func (a *OverlayAdapter) renameScratchLocked(oldKey, newKey CanonicalKey, newTarget pythonruntime.CanonicalTarget) pythonruntime.Errno {
	n, ok := a.scratch[oldKey]
	if !ok || n.kind == nodeTombstone {
		return pythonruntime.ErrnoENOENT
	}
	if oldKey == newKey {
		return pythonruntime.ErrnoSuccess
	}
	delete(a.scratch, oldKey)
	n.mu.Lock()
	n.target = newTarget
	n.mu.Unlock()
	a.scratch[newKey] = n
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) renameWorkspaceLocked(ctx context.Context, parentOpID string, oldKey, newKey CanonicalKey, oldTarget, newTarget pythonruntime.CanonicalTarget) pythonruntime.Errno {
	oldNode, inOverlay := a.overlay[oldKey]
	if inOverlay && oldNode.kind == nodeTombstone {
		return pythonruntime.ErrnoENOENT
	}
	if !inOverlay {
		snap, snapErrno := a.pinHostStateLocked(ctx, oldTarget, PinOriginBlindWrite)
		if snapErrno != pythonruntime.ErrnoSuccess {
			return snapErrno
		}
		kind := nodeFile
		if snap.IsDir {
			kind = nodeDir
		} else if snap.IsSymlink {
			kind = nodeSymlink
		}
		orig := snap.Target
		oldNode = &overlayNode{
			kind:             kind,
			target:           snap.Target,
			data:             append([]byte(nil), snap.Bytes...),
			mode:             snap.Mode,
			modTime:          snap.ModTime,
			inode:            snap.Inode,
			linkTarget:       snap.LinkTarget,
			createdInOverlay: false,
			originTarget:     &orig,
			parentOpID:       parentOpID,
		}
	}
	if oldKey == newKey {
		return pythonruntime.ErrnoSuccess
	}

	_, _ = a.pinHostStateLocked(ctx, newTarget, PinOriginBlindWrite)
	if !oldNode.createdInOverlay && oldNode.originTarget == nil {
		orig := oldTarget
		oldNode.originTarget = &orig
	}
	if a.existedInHostLocked(ctx, oldTarget) {
		a.overlay[oldKey] = &overlayNode{
			kind:        nodeTombstone,
			target:      oldTarget,
			mutationSeq: a.allocSeqLocked(),
			parentOpID:  parentOpID,
		}
	} else {
		delete(a.overlay, oldKey)
	}

	oldNode.mu.Lock()
	oldNode.target = newTarget
	oldNode.modTime = a.now().UTC()
	if parentOpID != "" {
		oldNode.parentOpID = parentOpID
	}
	oldNode.mu.Unlock()
	oldNode.mutationSeq = a.allocSeqLocked()
	a.overlay[newKey] = oldNode

	prev := oldTarget
	a.recordOverlayEventLocked(parentOpID, "rename", newTarget, &prev, int64(len(oldNode.data)), oldNode.writeOriginFrame, nil)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Readlink(ctx context.Context, rawPath string) (string, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return "", errno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		return "", symErrno
	}
	linkTarget, isLink, rdErrno := a.readlinkSingleLocked(ctx, resolved)
	if rdErrno != pythonruntime.ErrnoSuccess {
		return "", rdErrno
	}
	if !isLink {
		return "", pythonruntime.ErrnoEINVAL
	}
	return linkTarget, pythonruntime.ErrnoSuccess
}

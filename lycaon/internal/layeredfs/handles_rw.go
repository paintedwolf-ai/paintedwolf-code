package layeredfs

import (
	"io"
	"syscall"
	"time"
)

// ReadHandle reads bytes from an open handle at the specified offset.
// Handles remain valid across rename and open-unlink until closed or the view is released.
func (c *Core) ReadHandle(hid HandleID, p []byte, offset int64) (int, error) {
	c.mu.RLock()
	h, node, err := validateHandleLocked(c, hid, false)
	if err != nil {
		c.mu.RUnlock()
		return 0, err
	}
	file := h.file
	roReader := h.roReader
	qpath := h.qpath
	layer := LayerPrivate
	nodeID, gen := h.nodeID, h.gen
	if node != nil {
		nodeID, gen = node.id, node.gen
	} else {
		layer = LayerTrunk
	}
	c.mu.RUnlock()

	var n int
	if file != nil {
		n, err = file.ReadAt(p, offset)
	} else if roReader != nil {
		n, err = roReader.ReadAt(p, offset)
	} else {
		return 0, syscall.EBADF
	}
	if n > 0 && err == io.EOF {
		err = nil
	}
	if n > 0 {
		emitObservation(c, ObservationEvent{
			Path:       qpath,
			Layer:      layer,
			NodeID:     nodeID,
			Generation: gen,
			Kind:       ObserveRead,
			Size:       int64(n),
		})
	}
	return n, err
}

// WriteHandle writes bytes to an open handle at the specified offset.
// Also supports NFS owner-created read-only loose object writes (#1660) via the valid handle.
func (c *Core) WriteHandle(hid HandleID, data []byte, offset int64) (int, error) {
	if err := c.barrier.enterMutation(); err != nil {
		return 0, err
	}
	defer c.barrier.leaveMutation()

	c.mu.Lock()
	h, node, err := validateHandleLocked(c, hid, true)
	if err != nil {
		c.mu.Unlock()
		return 0, err
	}
	file := h.file
	if h.flags&OpenAppend != 0 {
		offset = node.size
	}
	c.mu.Unlock()

	if len(data) == 0 {
		return 0, nil
	}

	n, wErr := file.WriteAt(data, offset)
	if wErr != nil {
		return n, wErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return n, ErrViewReleased
	}
	if offset+int64(n) > node.size {
		node.size = offset + int64(n)
	}
	now := time.Now()
	node.mtime = now
	node.ctime = now
	node.flags.contentChanged = true
	recordJournalLocked(c, JournalOpWrite, h.qpath, nil, node, int64(n))
	return n, nil
}

// StatHandle returns current attributes for the object bound to hid, even if renamed or unlinked.
func (c *Core) StatHandle(hid HandleID) (EntryAttr, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	h, node, err := validateHandleLocked(c, hid, false)
	if err != nil {
		return EntryAttr{}, err
	}
	if node != nil {
		return nodeToAttrLocked(node), nil
	}
	if h.roReader != nil {
		if osH, ok := h.roReader.(*osFileReadHandle); ok && osH.file != nil {
			info, sErr := osH.file.Stat()
			if sErr != nil {
				return EntryAttr{}, sErr
			}
			return EntryAttr{
				NodeID:     h.nodeID,
				Generation: h.gen,
				Layer:      LayerTrunk,
				Kind:       EntryRegular,
				Mode:       info.Mode(),
				Size:       info.Size(),
				ModTime:    info.ModTime(),
				ATime:      info.ModTime(),
				CTime:      info.ModTime(),
				Nlink:      1,
			}, nil
		}
	}
	return EntryAttr{}, ErrStaleHandle
}

// SyncHandle flushes dirty data for hid to stable storage.
func (c *Core) SyncHandle(hid HandleID) error {
	c.mu.RLock()
	h, _, err := validateHandleLocked(c, hid, false)
	if err != nil {
		c.mu.RUnlock()
		return err
	}
	f := h.file
	c.mu.RUnlock()
	if f == nil {
		return nil
	}
	return syncFD(int(f.Fd()))
}

// CloseHandle closes and retires an open handle.
func (c *Core) CloseHandle(hid HandleID) error {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return ErrViewReleased
	}
	h, ok := c.state.handles[hid]
	if !ok || h.closed {
		c.mu.Unlock()
		return ErrStaleHandle
	}
	h.closed = true
	delete(c.state.handles, hid)
	node := c.state.nodesByID[h.nodeID]
	var unlinkOrphan string
	if node != nil && node.flags.unlinked && !hasOpenHandlesForNodeLocked(c, node.id) {
		unlinkOrphan = node.flags.backingName
	}
	c.mu.Unlock()

	var closeErr error
	if h.file != nil {
		closeErr = h.file.Close()
	}
	if h.roReader != nil {
		closeErr = h.roReader.Close()
	}
	if unlinkOrphan != "" && c.backing.workDirFile != nil {
		_ = unlinkBackingFile(int(c.backing.workDirFile.Fd()), unlinkOrphan)
	}
	return closeErr
}

// WriteViaNodeHandle supports protocol-level writes (like NFSv3 WRITE on an owner-created 0444 file)
// identified by (NodeID, Generation) without requiring a stateful open handle.
func (c *Core) WriteViaNodeHandle(nodeID NodeID, gen Generation, data []byte, offset int64) (int, EntryAttr, error) {
	if err := c.barrier.enterMutation(); err != nil {
		return 0, EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return 0, EntryAttr{}, ErrViewReleased
	}
	node, ok := c.state.nodesByID[nodeID]
	if !ok || node == nil || node.gen != gen || node.flags.unlinked {
		c.mu.Unlock()
		return 0, EntryAttr{}, ErrStaleHandle
	}
	if node.kind != EntryRegular {
		c.mu.Unlock()
		return 0, EntryAttr{}, syscall.EISDIR
	}
	if node.mode&0o200 == 0 && !node.flags.ownerCreatedRO {
		c.mu.Unlock()
		return 0, EntryAttr{}, syscall.EACCES
	}
	backingName, backingPath := node.flags.backingName, node.flags.backingPath
	var anyPath QualifiedPath
	for qp := range node.paths {
		anyPath = qp
		break
	}
	c.mu.Unlock()

	wf, err := openBackingFileRW(int(c.backing.workDirFile.Fd()), backingName, backingPath)
	if err != nil {
		return 0, EntryAttr{}, err
	}
	n, wErr := wf.WriteAt(data, offset)
	_ = wf.Close()
	if wErr != nil {
		return n, EntryAttr{}, wErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return n, EntryAttr{}, ErrViewReleased
	}
	if offset+int64(n) > node.size {
		node.size = offset + int64(n)
	}
	now := time.Now()
	node.mtime = now
	node.ctime = now
	node.flags.contentChanged = true
	recordJournalLocked(c, JournalOpWrite, anyPath, nil, node, int64(n))
	return n, nodeToAttrLocked(node), nil
}

func validateHandleLocked(c *Core, hid HandleID, requireWrite bool) (*openHandle, *privateNode, error) {
	if c.state.released {
		return nil, nil, ErrViewReleased
	}
	h, ok := c.state.handles[hid]
	if !ok || h.closed || h.viewID != c.cfg.ViewID {
		return nil, nil, ErrStaleHandle
	}
	if node, exists := c.state.nodesByID[h.nodeID]; exists {
		if node.gen != h.gen {
			return nil, nil, ErrStaleHandle
		}
		if requireWrite && !h.flags.hasWriteIntent() && !node.flags.ownerCreatedRO {
			return nil, nil, syscall.EBADF
		}
		return h, node, nil
	}
	if requireWrite {
		return nil, nil, ErrStaleHandle
	}
	return h, nil, nil
}

func hasOpenHandlesForNodeLocked(c *Core, nid NodeID) bool {
	for _, h := range c.state.handles {
		if !h.closed && h.nodeID == nid {
			return true
		}
	}
	return false
}

// OpenBackingReader opens a read-only stream to a private node's backing content by NodeID and Generation.
func (c *Core) OpenBackingReader(nodeID NodeID, gen Generation) (io.ReadCloser, error) {
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

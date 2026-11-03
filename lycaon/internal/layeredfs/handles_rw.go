package layeredfs

import (
	"io"
	"os"
	"syscall"
	"time"
)

// ReadHandle reads bytes from an open handle at the specified offset.
// Handles remain valid across rename and open-unlink until closed or the view is released.
func (c *Core) ReadHandle(hid HandleID, p []byte, offset int64) (int, error) {
	c.mu.Lock()
	h, node, err := validateHandleLocked(c, hid, false)
	if err != nil {
		c.mu.Unlock()
		return 0, err
	}
	h.refs++
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
	c.mu.Unlock()
	defer func() { _ = releaseHandleRef(c, h) }()

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
// Conflicting writes, appends, and truncates on the same node are serialized through commit (#1657).
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
	h.refs++
	qpath := h.qpath
	appendMode := h.flags&OpenAppend != 0
	c.mu.Unlock()
	defer func() { _ = releaseHandleRef(c, h) }()

	if len(data) == 0 {
		return 0, nil
	}

	if appendMode && c.cfg.FaultHooks != nil && c.cfg.FaultHooks.BeforeAppendLock != nil {
		c.cfg.FaultHooks.BeforeAppendLock(qpath, data)
	}

	node.flags.ioMu.Lock()
	defer node.flags.ioMu.Unlock()

	c.mu.Lock()
	if _, _, vErr := validateHandleLocked(c, hid, true); vErr != nil {
		c.mu.Unlock()
		return 0, vErr
	}
	writeOffset := offset
	if appendMode {
		writeOffset = node.size
	}
	qpath = h.qpath
	file := h.file
	c.mu.Unlock()

	n, wErr := writeBackingAt(c, qpath, file, data, writeOffset)
	if n > 0 {
		c.mu.Lock()
		if !c.state.released {
			if writeOffset+int64(n) > node.size {
				node.size = writeOffset + int64(n)
			}
			now := time.Now()
			node.mtime = now
			node.ctime = now
			node.flags.contentChanged = true
			recordJournalLocked(c, JournalOpWrite, h.qpath, nil, node, int64(n))
		}
		c.mu.Unlock()
	}
	return n, wErr
}

func writeBackingAt(c *Core, qpath QualifiedPath, file *os.File, data []byte, offset int64) (int, error) {
	if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.WriteAtFunc != nil {
		return c.cfg.FaultHooks.WriteAtFunc(qpath, file, data, offset)
	}
	return file.WriteAt(data, offset)
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
	c.mu.Lock()
	h, _, err := validateHandleLocked(c, hid, false)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	h.refs++
	f := h.file
	c.mu.Unlock()
	defer func() { _ = releaseHandleRef(c, h) }()

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
	c.mu.Unlock()
	return releaseHandleRef(c, h)
}

func releaseHandleRef(c *Core, h *openHandle) error {
	c.mu.Lock()
	h.refs--
	if h.refs > 0 {
		c.mu.Unlock()
		return nil
	}
	node := c.state.nodesByID[h.nodeID]
	var unlinkOrphan string
	var workDirFD = -1
	if node != nil && node.flags.unlinked && !hasOpenHandlesForNodeLocked(c, node.id) {
		unlinkOrphan = node.flags.backingName
		if c.backing.workDirFile != nil {
			workDirFD = int(c.backing.workDirFile.Fd())
		}
	}
	c.mu.Unlock()

	var closeErr error
	if h.file != nil {
		closeErr = h.file.Close()
	}
	if h.roReader != nil {
		closeErr = h.roReader.Close()
	}
	if unlinkOrphan != "" && workDirFD >= 0 && node != nil {
		node.flags.ioMu.Lock()
		_ = unlinkBackingFile(workDirFD, unlinkOrphan)
		node.flags.ioMu.Unlock()
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
	if c.state.released {
		return n, EntryAttr{}, ErrViewReleased
	}
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

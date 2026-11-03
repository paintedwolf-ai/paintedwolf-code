package layeredfs

import (
	"bytes"
	"io/fs"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Chmod updates permission and mode bits on qpath.
func (c *Core) Chmod(qpath QualifiedPath, mode fs.FileMode) (EntryAttr, error) {
	return c.SetAttr(qpath, SetAttrRequest{Mode: &mode})
}

// Chtimes updates access and modification timestamps on qpath.
func (c *Core) Chtimes(qpath QualifiedPath, atime, mtime time.Time, noFollow bool) (EntryAttr, error) {
	return c.SetAttr(qpath, SetAttrRequest{
		ATime:    &atime,
		MTime:    &mtime,
		NoFollow: noFollow,
	})
}

// SetAttr updates supported attributes on qpath.
// Ownership changes return EPERM/ENOTSUP per the capability contract (#1657, #1660).
func (c *Core) SetAttr(qpath QualifiedPath, req SetAttrRequest) (EntryAttr, error) {
	return setAttrPath(c, qpath, req)
}

func setAttrPath(c *Core, qpath QualifiedPath, req SetAttrRequest) (EntryAttr, error) {
	if req.UID != nil || req.GID != nil {
		return EntryAttr{}, syscall.EPERM
	}
	if req.Size != nil {
		attr, err := c.Truncate(qpath, *req.Size)
		if err != nil {
			return EntryAttr{}, err
		}
		if req.Mode == nil && req.ATime == nil && req.MTime == nil {
			return attr, nil
		}
	}
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	followFinal := !req.NoFollow
	res, err := resolvePath(c, qpath, followFinal, true)
	if err != nil {
		return EntryAttr{}, err
	}

	node, err := ensureCopiedUpNode(c, res.qpath)
	if err != nil {
		return EntryAttr{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return EntryAttr{}, ErrViewReleased
	}
	changed := false
	now := time.Now()
	if req.Mode != nil {
		if node.kind == EntrySymlink {
			return EntryAttr{}, syscall.EOPNOTSUPP
		}
		typeBits := node.mode & fs.ModeType
		newPerm := req.Mode.Perm() | (*req.Mode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky))
		newMode := typeBits | newPerm
		if newMode != node.mode {
			node.mode = newMode
			changed = true
		}
	}
	if req.ATime != nil {
		node.atime = *req.ATime
		changed = true
	}
	if req.MTime != nil {
		if !node.mtime.Equal(*req.MTime) {
			node.mtime = *req.MTime
			changed = true
		}
	}
	if changed {
		node.ctime = now
		node.flags.metadataChanged = !metadataMatchesInitialLocked(node)
		recordJournalLocked(c, JournalOpSetAttr, res.qpath, nil, node, 0)
	}
	return nodeToAttrLocked(node), nil
}

// GetXattr retrieves an extended attribute from qpath.
func (c *Core) GetXattr(qpath QualifiedPath, name string, noFollow bool) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, syscall.EINVAL
	}
	res, err := resolvePath(c, qpath, !noFollow, false)
	if err != nil {
		return nil, err
	}
	val, ok := res.attr.Xattrs[name]
	if !ok {
		return nil, syscall.ENODATA
	}
	return append([]byte(nil), val...), nil
}

// ListXattr lists all extended attribute names on qpath in sorted order.
func (c *Core) ListXattr(qpath QualifiedPath, noFollow bool) ([]string, error) {
	res, err := resolvePath(c, qpath, !noFollow, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.attr.Xattrs))
	for k := range res.attr.Xattrs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// SetXattr sets an extended attribute on qpath.
func (c *Core) SetXattr(qpath QualifiedPath, name string, value []byte, noFollow bool) error {
	return setXattrPath(c, qpath, name, value, noFollow)
}

func setXattrPath(c *Core, qpath QualifiedPath, name string, value []byte, noFollow bool) error {
	if strings.TrimSpace(name) == "" {
		return syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()

	res, err := resolvePath(c, qpath, !noFollow, true)
	if err != nil {
		return err
	}
	node, err := ensureCopiedUpNode(c, res.qpath)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	if node.xattrs == nil {
		node.xattrs = make(map[string][]byte)
	}
	node.xattrs[name] = append([]byte(nil), value...)
	node.ctime = time.Now()
	node.flags.metadataChanged = !metadataMatchesInitialLocked(node)
	recordJournalLocked(c, JournalOpSetXattr, res.qpath, nil, node, int64(len(value)))
	return nil
}

// RemoveXattr deletes an extended attribute from qpath.
func (c *Core) RemoveXattr(qpath QualifiedPath, name string, noFollow bool) error {
	return removeXattrPath(c, qpath, name, noFollow)
}

func removeXattrPath(c *Core, qpath QualifiedPath, name string, noFollow bool) error {
	if strings.TrimSpace(name) == "" {
		return syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()

	res, err := resolvePath(c, qpath, !noFollow, true)
	if err != nil {
		return err
	}
	node, err := ensureCopiedUpNode(c, res.qpath)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	if _, ok := node.xattrs[name]; !ok {
		return syscall.ENODATA
	}
	delete(node.xattrs, name)
	node.ctime = time.Now()
	node.flags.metadataChanged = !metadataMatchesInitialLocked(node)
	recordJournalLocked(c, JournalOpRemoveXattr, res.qpath, nil, node, 0)
	return nil
}

func metadataMatchesInitialLocked(n *privateNode) bool {
	if !n.flags.physicalCopyUp {
		return false
	}
	if n.mode != n.initial.mode || !n.mtime.Equal(n.initial.mtime) {
		return false
	}
	if len(n.xattrs) != len(n.initial.xattrs) {
		return false
	}
	for k, v := range n.xattrs {
		iv, ok := n.initial.xattrs[k]
		if !ok || !bytes.Equal(v, iv) {
			return false
		}
	}
	return true
}

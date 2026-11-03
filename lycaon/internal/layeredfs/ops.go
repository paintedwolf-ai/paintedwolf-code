package layeredfs

import (
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"
)

// CreateFile creates and opens a file, replacing or truncating if requested.
func (c *Core) CreateFile(qpath QualifiedPath, mode fs.FileMode, exclusive bool) (HandleID, EntryAttr, error) {
	flags := OpenReadWrite | OpenCreate
	if exclusive {
		flags |= OpenExclusive
	} else {
		flags |= OpenTruncate
	}
	return c.Open(qpath, flags, mode)
}

// WriteFile writes content atomically or in place to qpath through the layered core.
func (c *Core) WriteFile(qpath QualifiedPath, data []byte, mode fs.FileMode) (EntryAttr, error) {
	hid, _, err := c.Open(qpath, OpenWriteOnly|OpenCreate|OpenTruncate, mode)
	if err != nil {
		return EntryAttr{}, err
	}
	if len(data) > 0 {
		if _, wErr := c.WriteHandle(hid, data, 0); wErr != nil {
			_ = c.CloseHandle(hid)
			return EntryAttr{}, wErr
		}
	}
	attr, sErr := c.StatHandle(hid)
	closeErr := c.CloseHandle(hid)
	if sErr != nil {
		return EntryAttr{}, sErr
	}
	return attr, closeErr
}

// ReadFile reads the entire contents of qpath from whichever layer owns it.
func (c *Core) ReadFile(qpath QualifiedPath) ([]byte, error) {
	hid, attr, err := c.Open(qpath, OpenReadOnly, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.CloseHandle(hid) }()
	if attr.Kind != EntryRegular {
		return nil, syscall.EISDIR
	}
	buf := make([]byte, attr.Size)
	if attr.Size == 0 {
		return buf, nil
	}
	n, rErr := c.ReadHandle(hid, buf, 0)
	if rErr != nil && n == 0 {
		return nil, rErr
	}
	return buf[:n], nil
}

// Truncate changes the size of the file at qpath.
func (c *Core) Truncate(qpath QualifiedPath, size int64) (EntryAttr, error) {
	return truncatePath(c, qpath, size)
}

func truncatePath(c *Core, qpath QualifiedPath, size int64) (EntryAttr, error) {
	if size < 0 {
		return EntryAttr{}, syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	res, err := resolvePath(c, qpath, true, true)
	if err != nil {
		return EntryAttr{}, err
	}
	if res.attr.Kind == EntryDirectory {
		return EntryAttr{}, syscall.EISDIR
	}
	if res.attr.Kind != EntryRegular {
		return EntryAttr{}, syscall.EINVAL
	}

	base, err := ensureBaseCaptured(c, res.qpath)
	if err != nil {
		return EntryAttr{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return EntryAttr{}, ErrViewReleased
	}
	node, err := ensureWritableNodeLocked(c, res.qpath, base, OpenWriteOnly, res.attr.Mode)
	if err != nil {
		return EntryAttr{}, err
	}
	wf, err := openBackingFileRW(int(c.backing.workDirFile.Fd()), node.flags.backingName, node.flags.backingPath)
	if err != nil {
		return EntryAttr{}, err
	}
	tErr := wf.Truncate(size)
	_ = wf.Close()
	if tErr != nil {
		return EntryAttr{}, tErr
	}
	now := time.Now()
	node.size = size
	node.mtime = now
	node.ctime = now
	node.flags.contentChanged = true
	recordJournalLocked(c, JournalOpTruncate, res.qpath, nil, node, size)
	return nodeToAttrLocked(node), nil
}

// Mkdir creates a new directory at qpath.
func (c *Core) Mkdir(qpath QualifiedPath, mode fs.FileMode) (EntryAttr, error) {
	return mkdirPath(c, qpath, mode)
}

func mkdirPath(c *Core, qpath QualifiedPath, mode fs.FileMode) (EntryAttr, error) {
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	parentPath, name, err := resolveParentDir(c, qpath, true)
	if err != nil {
		return EntryAttr{}, err
	}
	targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
	targetPath, err = checkGitAccess(c, targetPath, true)
	if err != nil {
		return EntryAttr{}, err
	}

	if _, lookErr := lookupExactComponent(c, targetPath); lookErr == nil {
		return EntryAttr{}, os.ErrExist
	} else if !os.IsNotExist(lookErr) {
		return EntryAttr{}, lookErr
	}

	base, err := ensureBaseCaptured(c, targetPath)
	if err != nil {
		return EntryAttr{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return EntryAttr{}, ErrViewReleased
	}
	if _, lookID := c.state.pathIndex[targetPath]; lookID {
		return EntryAttr{}, os.ErrExist
	}

	now := time.Now()
	nid, gen := c.state.allocNodeID()
	cleanMode := fs.ModeDir | (mode.Perm() & 0o777)
	if cleanMode.Perm() == 0 {
		cleanMode = fs.ModeDir | 0o755
	}
	node := &privateNode{
		id:    nid,
		gen:   gen,
		kind:  EntryDirectory,
		mode:  cleanMode,
		atime: now,
		mtime: now,
		ctime: now,
		paths: map[QualifiedPath]struct{}{targetPath: {}},
		flags: nodeFlags{namespaceAdded: true},
	}
	_, wasReplaced := c.state.deletedPaths[targetPath]
	delete(c.state.deletedPaths, targetPath)
	c.state.nodesByID[nid] = node
	c.state.pathIndex[targetPath] = nid
	bumpParentDirGenLocked(c, targetPath)
	opKind := JournalOpMkdir
	if wasReplaced || (base.Kind != EntryAbsent && base.Kind != EntryDirectory) {
		opKind = JournalOpReplaceDir
	}
	recordJournalLocked(c, opKind, targetPath, nil, node, 0)
	return nodeToAttrLocked(node), nil
}

// Symlink creates a symbolic link at linkPath pointing to target.
func (c *Core) Symlink(target string, linkPath QualifiedPath) (EntryAttr, error) {
	return symlinkPath(c, target, linkPath)
}

func symlinkPath(c *Core, target string, linkPath QualifiedPath) (EntryAttr, error) {
	if strings.TrimSpace(target) == "" || strings.ContainsRune(target, 0) {
		return EntryAttr{}, syscall.EINVAL
	}
	if err := c.barrier.enterMutation(); err != nil {
		return EntryAttr{}, err
	}
	defer c.barrier.leaveMutation()

	parentPath, name, err := resolveParentDir(c, linkPath, true)
	if err != nil {
		return EntryAttr{}, err
	}
	targetPath := QualifiedPath{RootID: parentPath.RootID, Rel: joinRel(parentPath.Rel, name)}
	targetPath, err = checkGitAccess(c, targetPath, true)
	if err != nil {
		return EntryAttr{}, err
	}

	if _, lookErr := lookupExactComponent(c, targetPath); lookErr == nil {
		return EntryAttr{}, os.ErrExist
	} else if !os.IsNotExist(lookErr) {
		return EntryAttr{}, lookErr
	}

	base, err := ensureBaseCaptured(c, targetPath)
	if err != nil {
		return EntryAttr{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return EntryAttr{}, ErrViewReleased
	}
	now := time.Now()
	nid, gen := c.state.allocNodeID()
	node := &privateNode{
		id:    nid,
		gen:   gen,
		kind:  EntrySymlink,
		mode:  fs.ModeSymlink | 0o777,
		size:  int64(len(target)),
		atime: now,
		mtime: now,
		ctime: now,
		paths: map[QualifiedPath]struct{}{targetPath: {}},
		flags: nodeFlags{
			contentChanged: true,
			namespaceAdded: true,
			linkTarget:     target,
		},
	}
	_, wasReplaced := c.state.deletedPaths[targetPath]
	delete(c.state.deletedPaths, targetPath)
	c.state.nodesByID[nid] = node
	c.state.pathIndex[targetPath] = nid
	bumpParentDirGenLocked(c, targetPath)
	opKind := JournalOpSymlink
	if wasReplaced || base.Kind == EntryDirectory {
		opKind = JournalOpReplaceDir
	}
	recordJournalLocked(c, opKind, targetPath, nil, node, int64(len(target)))
	return nodeToAttrLocked(node), nil
}

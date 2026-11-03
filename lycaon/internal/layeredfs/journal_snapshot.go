package layeredfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

func (b *backingWorkspace) withWorkDirFD(fn func(dirFD int) error) error {
	b.mu.RLock()
	if b.workDirFile != nil {
		fd := int(b.workDirFile.Fd())
		err := fn(fd)
		b.mu.RUnlock()
		return err
	}
	workDir := b.workDir
	b.mu.RUnlock()
	tmp, err := openDirNoFollow(workDir)
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close() }()
	return fn(int(tmp.Fd()))
}

func (b *backingWorkspace) withJournalDirFD(fn func(dirFD int, dirPath string) error) error {
	b.mu.RLock()
	if b.journalDirFile != nil {
		fd := int(b.journalDirFile.Fd())
		dirPath := b.journalDir
		err := fn(fd, dirPath)
		b.mu.RUnlock()
		return err
	}
	dirPath := b.journalDir
	b.mu.RUnlock()
	tmp, err := openDirNoFollow(dirPath)
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close() }()
	return fn(int(tmp.Fd()), dirPath)
}

func hashWorkingFileLocked(c *Core, qp QualifiedPath, bName, bPath string) (string, error) {
	var sumHex string
	err := c.backing.withWorkDirFD(func(dirFD int) error {
		f, err := openBackingFileRO(dirFD, bName, bPath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r := io.Reader(f)
		if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.WorkingFileHashReader != nil {
			var hErr error
			r, hErr = c.cfg.FaultHooks.WorkingFileHashReader(qp, f)
			if hErr != nil {
				return hErr
			}
		}
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		sumHex = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return sumHex, nil
}

func countLogicalChangesLocked(c *Core) int {
	count := 0
	for _, qp := range collectTouchedPathsLocked(c) {
		if isPathLogicallyChangedFastLocked(c, qp) {
			count++
		}
	}
	return count
}

func isPathLogicallyChangedFastLocked(c *Core, qp QualifiedPath) bool {
	base := c.state.basesByPath[qp]
	if nid, ok := c.state.pathIndex[qp]; ok {
		node := c.state.nodesByID[nid]
		if node != nil && !node.flags.unlinked {
			if !node.flags.physicalCopyUp || base.Kind == EntryAbsent || base.Kind != node.kind {
				return true
			}
			if node.flags.namespaceAdded || len(node.paths) > 1 || (node.initial.originPath != nil && *node.initial.originPath != qp) {
				return true
			}
			return node.flags.contentChanged || !metadataMatchesInitialLocked(node)
		}
	}
	if _, isDel := c.state.deletedPaths[qp]; isDel {
		return base.Kind != EntryAbsent
	}
	return false
}

// SnapshotJournal produces a versioned JournalSnapshot and persists it to the view's journal directory.
func (c *Core) SnapshotJournal() (JournalSnapshot, error) {
	return snapshotJournal(c)
}

func snapshotJournal(c *Core) (JournalSnapshot, error) {
	changes, err := c.EnumerateChanges(false)
	if err != nil {
		return JournalSnapshot{}, err
	}
	c.mu.RLock()
	ops := make([]JournalOperation, len(c.state.journal))
	copy(ops, c.state.journal)
	bases := make(map[string]BaseCapture, len(c.state.basesByPath))
	for qp, b := range c.state.basesByPath {
		bases[qp.RootID+":"+qp.Rel] = cloneBaseCapture(b)
	}
	c.mu.RUnlock()

	snap := JournalSnapshot{
		FormatVersion: FormatVersion,
		ViewID:        c.cfg.ViewID,
		Operations:    ops,
		Changes:       changes,
		Bases:         bases,
	}
	err = c.backing.withJournalDirFD(func(dirFD int, dirPath string) error {
		return persistJournalSnapshot(dirFD, dirPath, snap)
	})
	if err != nil {
		return JournalSnapshot{}, err
	}
	return snap, nil
}

func persistJournalSnapshot(dirFD int, dirPath string, snap JournalSnapshot) error {
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmpName := "journal.json.tmp"
	tmpPath := filepath.Join(dirPath, tmpName)
	_ = unlinkBackingFile(dirFD, tmpName)
	f, err := createExclusiveBackingFile(dirFD, tmpName, tmpPath, 0o600)
	if err != nil {
		return err
	}
	_, wErr := io.Copy(f, bytes.NewReader(raw))
	sErr := syncFD(int(f.Fd()))
	cErr := f.Close()
	if wErr != nil || sErr != nil || cErr != nil {
		_ = unlinkBackingFile(dirFD, tmpName)
		if wErr != nil {
			return wErr
		}
		if sErr != nil {
			return sErr
		}
		return cErr
	}
	return renameBackingFile(dirFD, tmpName, "journal.json")
}

// ParseJournalSnapshot decodes and validates a persisted versioned journal payload.
func ParseJournalSnapshot(raw []byte) (JournalSnapshot, error) {
	var snap JournalSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return JournalSnapshot{}, err
	}
	if snap.FormatVersion != FormatVersion {
		return JournalSnapshot{}, fmt.Errorf("layeredfs: unsupported journal format version %d", snap.FormatVersion)
	}
	return snap, nil
}

// LoadPersistedJournal reads and validates the persisted journal from the view's backing directory.
func (c *Core) LoadPersistedJournal() (JournalSnapshot, error) {
	var raw []byte
	err := c.backing.withJournalDirFD(func(dirFD int, dirPath string) error {
		f, oErr := openBackingFileRO(dirFD, "journal.json", filepath.Join(dirPath, "journal.json"))
		if oErr != nil {
			return oErr
		}
		defer func() { _ = f.Close() }()
		var rErr error
		raw, rErr = io.ReadAll(f)
		return rErr
	})
	if err != nil {
		return JournalSnapshot{}, err
	}
	return ParseJournalSnapshot(raw)
}

func initWorkingFromBase(c *Core, base BaseCapture) (string, string, error) {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return "", "", ErrViewReleased
	}
	workName, workPath := c.state.allocStorageFile(c.backing.workDir, "work")
	workDirFD := int(c.backing.workDirFile.Fd())
	c.mu.Unlock()

	if base.BackingFile != "" {
		cloned, cloneErr := cloneFileIfSupported(base.BackingFile, workPath)
		if cloneErr == nil && cloned {
			_ = syncFD(workDirFD)
			return workName, workPath, nil
		}
		baseFile, err := openBackingFileRO(int(c.backing.baseDirFile.Fd()), filepath.Base(base.BackingFile), base.BackingFile)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = baseFile.Close() }()

		wf, err := createExclusiveBackingFile(workDirFD, workName, workPath, 0o600)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(wf, baseFile)
		syncErr := syncFD(int(wf.Fd()))
		closeErr := wf.Close()
		dirSyncErr := syncFD(workDirFD)
		if err := errors.Join(copyErr, syncErr, closeErr, dirSyncErr); err != nil {
			_ = unlinkBackingFile(workDirFD, workName)
			return "", "", err
		}
		return workName, workPath, nil
	}

	wf, err := createExclusiveBackingFile(workDirFD, workName, workPath, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := errors.Join(wf.Close(), syncFD(workDirFD)); err != nil {
		_ = unlinkBackingFile(workDirFD, workName)
		return "", "", err
	}
	return workName, workPath, nil
}

func cloneXattrs(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

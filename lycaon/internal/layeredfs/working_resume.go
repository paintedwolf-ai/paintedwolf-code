package layeredfs

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrWorkingStateUnusable is working storage that cannot be resumed: its log is missing, from
// another format, or does not describe the files beside it. Recovery then starts from the view's
// last checkpoint.
var ErrWorkingStateUnusable = errors.New("layeredfs: working state cannot be resumed")

// ResumeReport says how much of a view's working state was restored.
type ResumeReport struct {
	FromSnapshot bool
	Records      int
	// TruncatedBytes is a torn or unreadable tail the replay stopped before. Every record before it
	// was applied.
	TruncatedBytes int64
}

// WorkingStateRecorded reports whether a view's working storage at backingDir keeps a working log,
// which Resume needs.
func WorkingStateRecorded(backingDir string) bool {
	info, err := os.Lstat(filepath.Join(backingDir, "journal", workingLogName))
	return err == nil && info.Mode().IsRegular()
}

// WorkingLogPaths names every path the working state at backingDir records, from its snapshot and
// every complete record. complete is false when there is no usable log or its tail is torn.
func WorkingLogPaths(backingDir string) ([]QualifiedPath, bool) {
	if backingDir == "" || !WorkingStateRecorded(backingDir) {
		return nil, false
	}
	journal := filepath.Join(backingDir, "journal")
	logRaw, err := os.ReadFile(filepath.Join(journal, workingLogName))
	if err != nil {
		return nil, false
	}
	seen := map[QualifiedPath]struct{}{}
	add := func(paths []workingPath, nodes []workingNode) {
		for _, p := range paths {
			seen[p.Path] = struct{}{}
		}
		for _, n := range nodes {
			for _, p := range n.Paths {
				seen[p] = struct{}{}
			}
		}
	}
	if snapRaw, err := os.ReadFile(filepath.Join(journal, workingSnapshotName)); err == nil {
		var snap workingSnapshot
		if json.Unmarshal(snapRaw, &snap) != nil || snap.Format != workingStateFormat {
			return nil, false
		}
		add(snap.Paths, snap.Nodes)
	}
	for tail := logRaw; len(tail) > 0; {
		rec, n, ok := decodeWorkingRecord(tail)
		if !ok || rec.Format != workingStateFormat {
			return nil, false
		}
		add(rec.Paths, rec.Nodes)
		tail = tail[n:]
	}
	out := make([]QualifiedPath, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sortPaths(out)
	return out, true
}

// Resume reopens the view whose working storage is at cfg.BackingDir, as of the last complete
// record its log holds. It is for an engine that exited: the bytes and names it restores are what
// the kernel held, which a power loss does not keep.
func Resume(cfg Config) (*Core, ResumeReport, error) {
	c, err := newCore(cfg)
	if err != nil {
		return nil, ResumeReport{}, err
	}
	report, err := c.loadWorkingState()
	if err != nil {
		c.roots.close()
		c.backing.close()
		return nil, report, errors.Join(ErrWorkingStateUnusable, err)
	}
	return c, report, nil
}

func (c *Core) loadWorkingState() (ResumeReport, error) {
	var report ResumeReport
	var snapRaw, logRaw []byte
	err := c.backing.withJournalDirFD(func(dirFD int, _ string) error {
		var err error
		if snapRaw, err = readAt(dirFD, workingSnapshotName); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		logRaw, err = readAt(dirFD, workingLogName)
		return err
	})
	if err != nil {
		return report, err
	}
	var applied uint64
	if snapRaw != nil {
		var snap workingSnapshot
		if err := json.Unmarshal(snapRaw, &snap); err != nil {
			return report, err
		}
		if snap.Format != workingStateFormat {
			return report, fmt.Errorf("snapshot format %d", snap.Format)
		}
		c.state.applyAlloc(snap.Alloc)
		c.state.applyPaths(snap.Paths, c.backing.baseDir)
		c.state.applyNodes(snap.Nodes)
		c.state.journal = append(c.state.journal, snap.Ops...)
		applied, report.FromSnapshot = snap.Seq, true
	}
	valid, tail := int64(0), logRaw
	for len(tail) > 0 {
		rec, n, ok := decodeWorkingRecord(tail)
		if !ok || rec.Format != workingStateFormat {
			break
		}
		switch {
		case rec.Seq <= applied:
		case rec.Seq == applied+1:
			c.state.applyAlloc(rec.Alloc)
			c.state.applyPaths(rec.Paths, c.backing.baseDir)
			c.state.applyNodes(rec.Nodes)
			c.state.journal = append(c.state.journal, rec.Ops...)
			applied = rec.Seq
			report.Records++
		default:
			return report, fmt.Errorf("log skips from record %d to %d", applied, rec.Seq)
		}
		valid += int64(n)
		tail = tail[n:]
	}
	report.TruncatedBytes = int64(len(logRaw)) - valid
	if err := c.state.settleResumed(c.backing.workDir); err != nil {
		return report, err
	}
	c.wal.seq, c.wal.size, c.wal.snapshotN = applied, valid, int64(len(snapRaw))
	if report.TruncatedBytes > 0 {
		// New records follow the last complete one, not the torn bytes after it.
		err := c.backing.withJournalDirFD(func(dirFD int, _ string) error {
			fd, err := unix.Openat(dirFD, workingLogName, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			return errors.Join(unix.Ftruncate(fd, valid), unix.Close(fd))
		})
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func readAt(dirFD int, name string) ([]byte, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// decodeWorkingRecord reads one framed record; ok is false for a torn or damaged frame.
func decodeWorkingRecord(raw []byte) (workingRecord, int, bool) {
	if len(raw) < workingRecordHeader {
		return workingRecord{}, 0, false
	}
	size := int(binary.BigEndian.Uint32(raw[0:4]))
	if size > len(raw)-workingRecordHeader {
		return workingRecord{}, 0, false
	}
	payload := raw[workingRecordHeader : workingRecordHeader+size]
	if crc32.Checksum(payload, workingLogCRC) != binary.BigEndian.Uint32(raw[4:8]) {
		return workingRecord{}, 0, false
	}
	var rec workingRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		return workingRecord{}, 0, false
	}
	return rec, workingRecordHeader + size, true
}

func (s *viewState) applyAlloc(a workingAlloc) {
	ids := &s.seq
	ids.nextNodeID, ids.nextGen = max(ids.nextNodeID, a.NodeID), max(ids.nextGen, a.Gen)
	ids.nextStorageID, ids.nextSeq = max(ids.nextStorageID, a.StorageID), max(ids.nextSeq, a.Seq)
}

// applyPaths takes each recorded path; a base file is named inside baseDir.
func (s *viewState) applyPaths(paths []workingPath, baseDir string) {
	for _, p := range paths {
		if p.Node != 0 {
			s.pathIndex[p.Path] = p.Node
		} else {
			delete(s.pathIndex, p.Path)
		}
		if p.Deleted != nil {
			s.deletedPaths[p.Path] = *p.Deleted
		} else {
			delete(s.deletedPaths, p.Path)
		}
		if p.Base != nil {
			base := cloneBaseCapture(*p.Base)
			if p.BaseFile != "" {
				base.BackingFile = filepath.Join(baseDir, filepath.Base(p.BaseFile))
			}
			s.basesByPath[p.Path] = base
		} else {
			delete(s.basesByPath, p.Path)
		}
	}
}

func (s *viewState) applyNodes(nodes []workingNode) {
	for _, r := range nodes {
		if r.Gone {
			delete(s.nodesByID, r.ID)
			continue
		}
		n := &privateNode{
			id: r.ID, origin: r.Origin, gen: r.Gen, kind: r.Kind, mode: r.Mode, size: r.Size,
			atime: r.ATime, mtime: r.MTime, ctime: r.CTime, xattrs: cloneXattrs(r.Xattrs),
			paths: make(map[QualifiedPath]struct{}, len(r.Paths)),
			initial: nodeInitialState{
				sha256: r.Initial.SHA256, size: r.Initial.Size, mode: r.Initial.Mode, mtime: r.Initial.MTime,
				xattrs: cloneXattrs(r.Initial.Xattrs), originPath: r.Initial.OriginPath,
			},
		}
		n.flags.unlinked, n.flags.physicalCopyUp = r.Unlinked, r.PhysicalCopyUp
		n.flags.contentChanged, n.flags.metadataChanged, n.flags.namespaceAdded = r.ContentChanged, r.MetadataChanged, r.NamespaceAdded
		n.flags.backingName, n.flags.gitHostPath, n.flags.linkTarget = r.BackingName, r.GitHostPath, r.LinkTarget
		for _, qpath := range r.Paths {
			n.paths[qpath] = struct{}{}
		}
		s.nodesByID[r.ID] = n
	}
}

// settleResumed drops nodes only an open handle kept, checks that every name reaches a node
// whose bytes are present, and takes each written file's size and time from its backing file,
// since writes reach it without a record.
func (s *viewState) settleResumed(workDir string) error {
	live := map[NodeID]bool{}
	for qpath, nid := range s.pathIndex {
		n := s.nodesByID[nid]
		if n == nil {
			return fmt.Errorf("%s names missing node %d", qpath.Rel, nid)
		}
		live[nid] = true
	}
	for nid, n := range s.nodesByID {
		if !live[nid] {
			delete(s.nodesByID, nid)
			continue
		}
		for qpath := range n.paths {
			if s.pathIndex[qpath] != nid {
				delete(n.paths, qpath)
			}
		}
		if n.origin != 0 {
			s.originIndex[n.origin] = nid
		}
		if n.kind != EntryRegular {
			continue
		}
		switch {
		case n.flags.gitHostPath != "":
			n.flags.backingPath = n.flags.gitHostPath
		case n.flags.backingName != "":
			n.flags.backingPath = filepath.Join(workDir, n.flags.backingName)
		default:
			return fmt.Errorf("file node %d has no backing", nid)
		}
		info, err := os.Lstat(n.flags.backingPath)
		if err != nil {
			return err
		}
		// Every recorded change sets ctime, so a backing file written later is newer than it.
		if info.Size() != n.size || info.ModTime().After(n.ctime) {
			n.size, n.mtime, n.ctime, n.flags.contentChanged = info.Size(), info.ModTime(), info.ModTime(), true
		}
		s.markDirtyBackingFile(n.flags.backingPath)
	}
	s.rebuildSpellings()
	return nil
}

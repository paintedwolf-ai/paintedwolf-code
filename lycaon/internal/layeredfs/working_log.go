package layeredfs

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// A mounted view's state lives in memory, so working storage alone would lose the names its bytes
// sit under when the engine exits. Each mutation therefore appends one record to a log in the
// view's journal directory as it leaves: the current value of every path and node it touched, and
// the journal operations it added. Replaying the records over the last snapshot reproduces the
// state as of the last complete record. Nothing is flushed per record; an engine exit keeps what
// the kernel holds, and only a published checkpoint survives power loss.

const (
	workingLogName      = "working.log"
	workingSnapshotName = "working-state.json"
	// workingStateFormat versions the snapshot and every log record.
	workingStateFormat = 1
	// workingLogCompactBytes is the log size past which a mutation folds the log into a snapshot.
	workingLogCompactBytes = 4 << 20
	workingRecordHeader    = 8
)

var workingLogCRC = crc32.MakeTable(crc32.Castagnoli)

// workingLog is a view's append-only state log in its journal directory. mu guards the file and its
// sequence and is taken while Core.mu is held, so records reach the file in state order.
type workingLog struct {
	backing *backingWorkspace
	records *atomic.Int64

	mu        sync.Mutex
	file      *os.File
	seq       uint64
	size      int64
	snapshotN int64
	err       error
}

// notePath and noteNode record that a mutation changed qpath's entry or a node; Core.mu is held.
func (s *viewState) notePath(qpath QualifiedPath) {
	s.pendingPaths[qpath] = struct{}{}
	if nid, ok := s.pathIndex[qpath]; ok {
		s.pendingNodes[nid] = struct{}{}
	}
}

func (s *viewState) noteNode(nid NodeID) {
	if nid != 0 {
		s.pendingNodes[nid] = struct{}{}
	}
}

func (s *viewState) setBase(qpath QualifiedPath, base BaseCapture) {
	s.basesByPath[qpath] = base
	s.notePath(qpath)
}

func (s *viewState) dropBase(qpath QualifiedPath) {
	delete(s.basesByPath, qpath)
	s.notePath(qpath)
}

// workingRecord is one mutation's effect. Paths and nodes carry their values after it; a path with
// no node, deletion or base, and a node marked gone, no longer exist.
type workingRecord struct {
	Format int                `json:"format"`
	Seq    uint64             `json:"seq"`
	Alloc  workingAlloc       `json:"alloc"`
	Paths  []workingPath      `json:"paths,omitempty"`
	Nodes  []workingNode      `json:"nodes,omitempty"`
	Ops    []JournalOperation `json:"ops,omitempty"`
}

type workingAlloc struct {
	NodeID    uint64 `json:"node_id"`
	Gen       uint64 `json:"gen"`
	StorageID uint64 `json:"storage_id"`
	Seq       uint64 `json:"seq"`
}

type workingPath struct {
	Path     QualifiedPath `json:"path"`
	Node     NodeID        `json:"node,omitempty"`
	Deleted  *deletedState `json:"deleted,omitempty"`
	Base     *BaseCapture  `json:"base,omitempty"`
	BaseFile string        `json:"base_file,omitempty"`
}

type workingNode struct {
	ID              NodeID            `json:"id"`
	Gone            bool              `json:"gone,omitempty"`
	Origin          NodeID            `json:"origin,omitempty"`
	Gen             Generation        `json:"gen,omitempty"`
	Kind            EntryKind         `json:"kind,omitempty"`
	Mode            fs.FileMode       `json:"mode,omitempty"`
	Size            int64             `json:"size,omitempty"`
	ATime           time.Time         `json:"atime"`
	MTime           time.Time         `json:"mtime"`
	CTime           time.Time         `json:"ctime"`
	Xattrs          map[string][]byte `json:"xattrs,omitempty"`
	Paths           []QualifiedPath   `json:"paths,omitempty"`
	Unlinked        bool              `json:"unlinked,omitempty"`
	PhysicalCopyUp  bool              `json:"physical_copy_up,omitempty"`
	ContentChanged  bool              `json:"content_changed,omitempty"`
	MetadataChanged bool              `json:"metadata_changed,omitempty"`
	NamespaceAdded  bool              `json:"namespace_added,omitempty"`
	BackingName     string            `json:"backing_name,omitempty"`
	GitHostPath     string            `json:"git_host_path,omitempty"`
	LinkTarget      string            `json:"link_target,omitempty"`
	Initial         workingInitial    `json:"initial"`
}

type workingInitial struct {
	SHA256     string            `json:"sha256,omitempty"`
	Size       int64             `json:"size,omitempty"`
	Mode       fs.FileMode       `json:"mode,omitempty"`
	MTime      time.Time         `json:"mtime"`
	Xattrs     map[string][]byte `json:"xattrs,omitempty"`
	OriginPath *QualifiedPath    `json:"origin_path,omitempty"`
}

// workingSnapshot is a view's whole state at log sequence Seq.
type workingSnapshot struct {
	Format int                `json:"format"`
	ViewID string             `json:"view_id"`
	Seq    uint64             `json:"seq"`
	Alloc  workingAlloc       `json:"alloc"`
	Paths  []workingPath      `json:"paths"`
	Nodes  []workingNode      `json:"nodes"`
	Ops    []JournalOperation `json:"ops"`
}

func (s *viewState) allocMarks() workingAlloc {
	a := s.seq
	return workingAlloc{NodeID: a.nextNodeID, Gen: a.nextGen, StorageID: a.nextStorageID, Seq: a.nextSeq}
}

func (s *viewState) pathRecord(qpath QualifiedPath) workingPath {
	rec := workingPath{Path: qpath, Node: s.pathIndex[qpath]}
	if state, ok := s.deletedPaths[qpath]; ok {
		rec.Deleted = &state
	}
	if base, ok := s.basesByPath[qpath]; ok {
		clone := cloneBaseCapture(base)
		rec.Base = &clone
		if base.BackingFile != "" {
			rec.BaseFile = filepath.Base(base.BackingFile)
		}
	}
	return rec
}

func (s *viewState) nodeRecord(nid NodeID) workingNode {
	n := s.nodesByID[nid]
	if n == nil {
		return workingNode{ID: nid, Gone: true}
	}
	rec := workingNode{
		ID: n.id, Origin: n.origin, Gen: n.gen, Kind: n.kind, Mode: n.mode, Size: n.size,
		ATime: n.atime, MTime: n.mtime, CTime: n.ctime, Xattrs: cloneXattrs(n.xattrs),
		Unlinked: n.flags.unlinked, PhysicalCopyUp: n.flags.physicalCopyUp, ContentChanged: n.flags.contentChanged,
		MetadataChanged: n.flags.metadataChanged, NamespaceAdded: n.flags.namespaceAdded,
		BackingName: n.flags.backingName, GitHostPath: n.flags.gitHostPath, LinkTarget: n.flags.linkTarget,
		Initial: workingInitial{
			SHA256: n.initial.sha256, Size: n.initial.size, Mode: n.initial.mode, MTime: n.initial.mtime,
			Xattrs: cloneXattrs(n.initial.xattrs), OriginPath: n.initial.originPath,
		},
	}
	for qpath := range n.paths {
		rec.Paths = append(rec.Paths, qpath)
	}
	sortPaths(rec.Paths)
	return rec
}

func sortPaths(paths []QualifiedPath) {
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].RootID != paths[j].RootID {
			return paths[i].RootID < paths[j].RootID
		}
		return paths[i].Rel < paths[j].Rel
	})
}

// endMutation appends the mutation's record, compacting the log once it is large, then lets a
// waiting barrier proceed.
func (c *Core) endMutation() {
	defer c.barrier.leaveMutation()
	c.mu.Lock()
	if c.state.released || (len(c.state.pendingPaths) == 0 && len(c.state.pendingNodes) == 0 && len(c.state.pendingOps) == 0) {
		c.mu.Unlock()
		return
	}
	rec := workingRecord{Format: workingStateFormat, Alloc: c.state.allocMarks(), Ops: c.state.pendingOps}
	for qpath := range c.state.pendingPaths {
		rec.Paths = append(rec.Paths, c.state.pathRecord(qpath))
	}
	for nid := range c.state.pendingNodes {
		rec.Nodes = append(rec.Nodes, c.state.nodeRecord(nid))
	}
	clear(c.state.pendingPaths)
	clear(c.state.pendingNodes)
	c.state.pendingOps = nil
	compact := false
	c.wal.mu.Lock()
	if c.wal.err != nil {
		// Records after a lost one would replay over a gap, so the log stops for good.
		c.mu.Unlock()
		c.wal.mu.Unlock()
		return
	}
	if c.wal.size > workingLogCompactBytes && c.wal.size > 2*c.wal.snapshotN {
		compact = true
	}
	var snap workingSnapshot
	if compact {
		c.wal.seq++
		snap = c.state.snapshot(c.cfg.ViewID, c.wal.seq)
	}
	c.mu.Unlock()
	defer c.wal.mu.Unlock()
	if compact {
		if err := c.wal.writeSnapshot(snap); err != nil {
			c.wal.abandon(err)
		}
		return
	}
	c.wal.seq++
	rec.Seq = c.wal.seq
	if err := c.wal.appendRecord(rec); err != nil {
		c.wal.abandon(err)
	}
}

// abandon removes the log and snapshot after a failed append, so recovery finds no
// working state to resume rather than a state with a hole in it.
func (l *workingLog) abandon(cause error) {
	l.err = cause
	_ = l.backing.withJournalDirFD(func(dirFD int, _ string) error {
		return errors.Join(unix.Unlinkat(dirFD, workingLogName, 0), unix.Unlinkat(dirFD, workingSnapshotName, 0))
	})
}

// snapshot captures the whole state as log sequence seq; Core.mu is held.
func (s *viewState) snapshot(viewID string, seq uint64) workingSnapshot {
	snap := workingSnapshot{
		Format: workingStateFormat, ViewID: viewID, Seq: seq, Alloc: s.allocMarks(),
		Ops: append([]JournalOperation(nil), s.journal...),
	}
	paths := map[QualifiedPath]struct{}{}
	for qpath := range s.pathIndex {
		paths[qpath] = struct{}{}
	}
	for qpath := range s.deletedPaths {
		paths[qpath] = struct{}{}
	}
	for qpath := range s.basesByPath {
		paths[qpath] = struct{}{}
	}
	for qpath := range paths {
		snap.Paths = append(snap.Paths, s.pathRecord(qpath))
	}
	for nid := range s.nodesByID {
		snap.Nodes = append(snap.Nodes, s.nodeRecord(nid))
	}
	return snap
}

// appendRecord writes one framed record with a single write; the log's mutex is held.
func (l *workingLog) appendRecord(rec workingRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	file, err := l.openFile()
	if err != nil {
		return err
	}
	frame := make([]byte, workingRecordHeader+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload))) //nolint:gosec // One mutation's record is far below 4 GiB.
	binary.BigEndian.PutUint32(frame[4:8], crc32.Checksum(payload, workingLogCRC))
	copy(frame[workingRecordHeader:], payload)
	n, err := file.Write(frame)
	l.size += int64(n)
	if err == nil {
		l.records.Add(1)
	}
	return err
}

// writeSnapshot replaces the snapshot, then empties the log the snapshot now covers. A crash
// between the two leaves records the replay skips by sequence.
func (l *workingLog) writeSnapshot(snap workingSnapshot) error {
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	err = l.backing.withJournalDirFD(func(dirFD int, dirPath string) error {
		tmp := fmt.Sprintf("%s.%d.tmp", workingSnapshotName, l.backing.tmpSeq.Add(1))
		fd, err := unix.Openat(dirFD, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), filepath.Join(dirPath, tmp))
		_, wErr := f.Write(raw)
		if err := errors.Join(wErr, f.Close()); err != nil {
			_ = unix.Unlinkat(dirFD, tmp, 0)
			return err
		}
		return unix.Renameat(dirFD, tmp, dirFD, workingSnapshotName)
	})
	if err != nil {
		return err
	}
	file, err := l.openFile()
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	l.size, l.snapshotN = 0, int64(len(raw))
	return nil
}

// openFile opens the log for appending on first use and after the backing was parked.
func (l *workingLog) openFile() (*os.File, error) {
	if l.file != nil {
		return l.file, nil
	}
	var file *os.File
	err := l.backing.withJournalDirFD(func(dirFD int, dirPath string) error {
		fd, err := unix.Openat(dirFD, workingLogName, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		file = os.NewFile(uintptr(fd), filepath.Join(dirPath, workingLogName))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err == nil {
		l.size = info.Size()
	}
	l.file = file
	return file, nil
}

// close closes the log file, as parking and release do.
func (l *workingLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// start begins an empty log, which marks the working storage as resumable from nothing.
func (l *workingLog) start() error {
	return l.backing.withJournalDirFD(func(dirFD int, _ string) error {
		if err := unix.Unlinkat(dirFD, workingSnapshotName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		fd, err := unix.Openat(dirFD, workingLogName, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		return unix.Close(fd)
	})
}

package pythondocuments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// ErrRevisionConflict indicates that a canonical workspace target's revision
// changed between its earliest pinned base and settlement publication.
var ErrRevisionConflict = errors.New(string(pythonruntime.StopRevisionConflict))

// RevisionConflictError wraps ErrRevisionConflict with structured conflict
// details for host settlement and receipts.
type RevisionConflictError struct {
	Conflict pythonruntime.RevisionConflict
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%s: target %s/%s expected %q, actual %q",
		pythonruntime.StopRevisionConflict,
		e.Conflict.Target.RootID,
		e.Conflict.Target.RelPath,
		e.Conflict.ExpectedRevision,
		e.Conflict.ActualRevision,
	)
}

func (e *RevisionConflictError) Unwrap() error {
	return ErrRevisionConflict
}

// DocumentRevision captures the host revision identity of a workspace document
// or its captured absence.
type DocumentRevision struct {
	RootID           string `json:"root_id,omitempty"`
	Branch           string `json:"branch,omitempty"`
	DocumentID       string `json:"document_id,omitempty"`
	DocumentRevision string `json:"document_revision,omitempty"`
	DiskSHA256       string `json:"disk_sha256,omitempty"`
	Absent           bool   `json:"absent,omitempty"`
	UnsavedDraft     bool   `json:"unsaved_draft,omitempty"`
}

// Identity returns a canonical string identifying this revision state.
// Including both DocumentRevision and DiskSHA256 ensures that a revision
// change is detected even if bytes later return to an earlier content hash.
func (r DocumentRevision) Identity() string {
	if r.Absent {
		if r.DocumentRevision != "" {
			return "absent@" + r.DocumentRevision
		}
		return "absent"
	}
	parts := make([]string, 0, 3)
	if r.DocumentID != "" {
		parts = append(parts, "doc="+r.DocumentID)
	}
	if r.DocumentRevision != "" {
		parts = append(parts, "rev="+r.DocumentRevision)
	}
	if r.DiskSHA256 != "" {
		parts = append(parts, "sha256="+r.DiskSHA256)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "|")
}

// HasIdentity reports whether r carries sufficient authoritative revision
// identity to advance a pin after commit.
func (r DocumentRevision) HasIdentity() bool {
	if r.Absent {
		return true
	}
	return r.DocumentRevision != "" || r.DiskSHA256 != ""
}

// Matches reports whether r and other represent the same host revision.
func (r DocumentRevision) Matches(other DocumentRevision) bool {
	if r.Absent != other.Absent {
		return false
	}
	if r.Absent {
		if r.DocumentRevision != "" && other.DocumentRevision != "" {
			return r.DocumentRevision == other.DocumentRevision
		}
		return true
	}
	if r.DocumentID != "" && other.DocumentID != "" && r.DocumentID != other.DocumentID {
		return false
	}
	if r.DocumentRevision != "" || other.DocumentRevision != "" {
		if r.DocumentRevision != other.DocumentRevision {
			return false
		}
	}
	if r.DiskSHA256 != "" && other.DiskSHA256 != "" && r.DiskSHA256 != other.DiskSHA256 {
		return false
	}
	return true
}

// RootSpec describes an attached workspace root and its worker branch/view.
type RootSpec struct {
	ID       string
	Label    string
	Branch   string
	Default  bool
	ReadOnly bool
}

// DocumentSnapshot is the host-captured representation of a workspace entry,
// including unsaved editor bytes when present.
type DocumentSnapshot struct {
	Target     pythonruntime.CanonicalTarget
	Revision   DocumentRevision
	Bytes      []byte
	Mode       fs.FileMode
	ModTime    time.Time
	IsDir      bool
	IsSymlink  bool
	LinkTarget string
	Inode      uint64
}

// PublishWriteRequest publishes a created or updated file through the host
// write door against its canonical earliest-base revision.
type PublishWriteRequest struct {
	Target       pythonruntime.CanonicalTarget
	BaseRevision DocumentRevision
	Bytes        []byte
	Mode         fs.FileMode
	Create       bool
}

// PublishDeleteRequest removes an existing file or directory through the host
// write door against its canonical earliest-base revision.
type PublishDeleteRequest struct {
	Target       pythonruntime.CanonicalTarget
	BaseRevision DocumentRevision
	IsDir        bool
}

// PublishMoveRequest moves an existing workspace entry to a destination path,
// optionally carrying modified bytes if the entry was edited in the overlay.
type PublishMoveRequest struct {
	Source             pythonruntime.CanonicalTarget
	SourceBaseRevision DocumentRevision
	Target             pythonruntime.CanonicalTarget
	TargetBaseRevision DocumentRevision
	HasModifiedBytes   bool
	Bytes              []byte
	Mode               fs.FileMode
}

// PublishMkdirRequest creates a directory through the host write door.
type PublishMkdirRequest struct {
	Target       pythonruntime.CanonicalTarget
	BaseRevision DocumentRevision
	Mode         fs.FileMode
}

// PublishResult reports the authoritative outcome of a host publication call.
type PublishResult struct {
	CommittedRevision DocumentRevision
	BytesWritten      int64
	Conflict          *pythonruntime.RevisionConflict
}

// DocumentStore is the host port bridging pythondocuments to Painted Wolf's
// editor/saved document capture, multi-root/worker views, and write-door
// publication.
type DocumentStore interface {
	ReadDocument(ctx context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno)
	StatDocument(ctx context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno)
	ListDir(ctx context.Context, target pythonruntime.CanonicalTarget) ([]pythonruntime.DirEntry, pythonruntime.Errno)
	PublishWrite(ctx context.Context, req PublishWriteRequest) (PublishResult, error)
	PublishDelete(ctx context.Context, req PublishDeleteRequest) (PublishResult, error)
	PublishMove(ctx context.Context, req PublishMoveRequest) (PublishResult, error)
	PublishMkdir(ctx context.Context, req PublishMkdirRequest) (PublishResult, error)
}

// SHA256Hex returns the lowercase hex SHA-256 digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MemoryPublishCall records a host publication attempt on MemoryDocumentStore.
type MemoryPublishCall struct {
	Operation string
	Target    pythonruntime.CanonicalTarget
	Source    *pythonruntime.CanonicalTarget
	Bytes     []byte
	Conflict  bool
}

type memoryEntry struct {
	docID        string
	revSeq       uint64
	savedBytes   []byte
	hasDraft     bool
	draftBytes   []byte
	mode         fs.FileMode
	modTime      time.Time
	isDir        bool
	isSymlink    bool
	linkTarget   string
	inode        uint64
	deletedRevID string
}

// MemoryDocumentStore is a thread-safe in-memory DocumentStore modeling saved
// disk bytes, unsaved editor drafts, monotonic document revisions, symlinks,
// multi-root views, and deterministic revision-conflict checking.
type MemoryDocumentStore struct {
	mu            sync.Mutex
	entries       map[CanonicalKey]*memoryEntry
	tombstoneRev  map[CanonicalKey]string
	nextRev       uint64
	nextDocID     uint64
	nextInode     uint64
	now           func() time.Time
	publishLog    []MemoryPublishCall
	BeforePublish func(op string, target pythonruntime.CanonicalTarget)
}

// NewMemoryDocumentStore creates an empty MemoryDocumentStore.
func NewMemoryDocumentStore() *MemoryDocumentStore {
	return &MemoryDocumentStore{
		entries:      make(map[CanonicalKey]*memoryEntry),
		tombstoneRev: make(map[CanonicalKey]string),
		nextInode:    1000,
		now:          time.Now,
	}
}

func (s *MemoryDocumentStore) allocRev() uint64 {
	s.nextRev++
	return s.nextRev
}

func (s *MemoryDocumentStore) allocDocID() string {
	s.nextDocID++
	return fmt.Sprintf("doc-%d", s.nextDocID)
}

func (s *MemoryDocumentStore) allocInode() uint64 {
	s.nextInode++
	return s.nextInode
}

func (s *MemoryDocumentStore) ensureParentDirsLocked(rootID, branch, rel string) {
	dir := path.Dir(cleanRelPath(rel))
	if dir == "." || dir == "" {
		return
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		k := CanonicalKey{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    rootID,
			Branch:    branch,
			RelPath:   cur,
		}
		if _, exists := s.entries[k]; !exists {
			s.entries[k] = &memoryEntry{
				docID:   s.allocDocID(),
				revSeq:  s.allocRev(),
				mode:    fs.ModeDir | 0o755,
				modTime: s.now().UTC(),
				isDir:   true,
				inode:   s.allocInode(),
			}
		}
	}
}

// SetSaved writes or updates a saved file on disk (preserving exact bytes,
// including BOM, CRLF, or binary data) and returns its authoritative revision.
func (s *MemoryDocumentStore) SetSaved(rootID, branch, rel string, data []byte, mode fs.FileMode) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel = cleanRelPath(rel)
	s.ensureParentDirsLocked(rootID, branch, rel)
	if mode == 0 {
		mode = 0o644
	}
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: rel}
	e, exists := s.entries[k]
	if !exists || e.isDir || e.isSymlink {
		e = &memoryEntry{
			docID: s.allocDocID(),
			inode: s.allocInode(),
		}
		s.entries[k] = e
	}
	delete(s.tombstoneRev, k)
	e.savedBytes = append([]byte(nil), data...)
	e.hasDraft = false
	e.draftBytes = nil
	e.mode = mode
	e.modTime = s.now().UTC()
	e.revSeq = s.allocRev()
	return s.revisionLocked(k, e)
}

// SetUnsavedDraft sets an unsaved editor draft over an existing (or new)
// document without modifying the underlying saved disk bytes.
func (s *MemoryDocumentStore) SetUnsavedDraft(rootID, branch, rel string, draft []byte) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel = cleanRelPath(rel)
	s.ensureParentDirsLocked(rootID, branch, rel)
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: rel}
	e, exists := s.entries[k]
	if !exists || e.isDir || e.isSymlink {
		e = &memoryEntry{
			docID: s.allocDocID(),
			mode:  0o644,
			inode: s.allocInode(),
		}
		s.entries[k] = e
	}
	delete(s.tombstoneRev, k)
	e.hasDraft = true
	e.draftBytes = append([]byte(nil), draft...)
	e.modTime = s.now().UTC()
	e.revSeq = s.allocRev()
	return s.revisionLocked(k, e)
}

// SimulateHumanEdit updates the live editor draft and advances the document
// revision, modeling a concurrent human edit during program execution.
func (s *MemoryDocumentStore) SimulateHumanEdit(rootID, branch, rel string, newBytes []byte) DocumentRevision {
	return s.SetUnsavedDraft(rootID, branch, rel, newBytes)
}

// ReplaceWithNewObject deletes and recreates a path with a fresh DocumentID
// and Inode, modeling replacement of a name by a new object.
func (s *MemoryDocumentStore) ReplaceWithNewObject(rootID, branch, rel string, data []byte) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel = cleanRelPath(rel)
	s.ensureParentDirsLocked(rootID, branch, rel)
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: rel}
	e := &memoryEntry{
		docID:      s.allocDocID(),
		revSeq:     s.allocRev(),
		savedBytes: append([]byte(nil), data...),
		mode:       0o644,
		modTime:    s.now().UTC(),
		inode:      s.allocInode(),
	}
	s.entries[k] = e
	delete(s.tombstoneRev, k)
	return s.revisionLocked(k, e)
}

// SetDir creates a directory entry in the store.
func (s *MemoryDocumentStore) SetDir(rootID, branch, rel string, mode fs.FileMode) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel = cleanRelPath(rel)
	s.ensureParentDirsLocked(rootID, branch, rel)
	if mode == 0 {
		mode = fs.ModeDir | 0o755
	}
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: rel}
	e := &memoryEntry{
		docID:   s.allocDocID(),
		revSeq:  s.allocRev(),
		mode:    mode | fs.ModeDir,
		modTime: s.now().UTC(),
		isDir:   true,
		inode:   s.allocInode(),
	}
	s.entries[k] = e
	delete(s.tombstoneRev, k)
	return s.revisionLocked(k, e)
}

// SetSymlink creates a symlink entry in the store.
func (s *MemoryDocumentStore) SetSymlink(rootID, branch, rel, linkTarget string) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel = cleanRelPath(rel)
	s.ensureParentDirsLocked(rootID, branch, rel)
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: rel}
	e := &memoryEntry{
		docID:      s.allocDocID(),
		revSeq:     s.allocRev(),
		mode:       fs.ModeSymlink | 0o777,
		modTime:    s.now().UTC(),
		isSymlink:  true,
		linkTarget: linkTarget,
		inode:      s.allocInode(),
	}
	s.entries[k] = e
	delete(s.tombstoneRev, k)
	return s.revisionLocked(k, e)
}

// SavedBytes returns the saved disk bytes for a path.
func (s *MemoryDocumentStore) SavedBytes(rootID, branch, rel string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: cleanRelPath(rel)}
	e, ok := s.entries[k]
	if !ok || e.isDir || e.isSymlink {
		return nil, false
	}
	return append([]byte(nil), e.savedBytes...), true
}

// EffectiveBytes returns the editor draft if present, else saved disk bytes.
func (s *MemoryDocumentStore) EffectiveBytes(rootID, branch, rel string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: cleanRelPath(rel)}
	e, ok := s.entries[k]
	if !ok || e.isDir || e.isSymlink {
		return nil, false
	}
	if e.hasDraft {
		return append([]byte(nil), e.draftBytes...), true
	}
	return append([]byte(nil), e.savedBytes...), true
}

// CurrentRevision returns the current DocumentRevision (or absent revision) for a path.
func (s *MemoryDocumentStore) CurrentRevision(rootID, branch, rel string) DocumentRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := CanonicalKey{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: cleanRelPath(rel)}
	return s.currentRevisionLocked(k)
}

// PublishLog returns a copy of all host publication calls recorded by this store.
func (s *MemoryDocumentStore) PublishLog() []MemoryPublishCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemoryPublishCall, len(s.publishLog))
	copy(out, s.publishLog)
	return out
}

func (s *MemoryDocumentStore) currentRevisionLocked(k CanonicalKey) DocumentRevision {
	e, ok := s.entries[k]
	if !ok {
		return DocumentRevision{
			RootID:           k.RootID,
			Branch:           k.Branch,
			DocumentRevision: s.tombstoneRev[k],
			Absent:           true,
		}
	}
	return s.revisionLocked(k, e)
}

func (s *MemoryDocumentStore) revisionLocked(k CanonicalKey, e *memoryEntry) DocumentRevision {
	var data []byte
	if e.hasDraft {
		data = e.draftBytes
	} else {
		data = e.savedBytes
	}
	sha := ""
	if !e.isDir {
		if e.isSymlink {
			sha = SHA256Hex([]byte(e.linkTarget))
		} else {
			sha = SHA256Hex(data)
		}
	}
	return DocumentRevision{
		RootID:           k.RootID,
		Branch:           k.Branch,
		DocumentID:       e.docID,
		DocumentRevision: fmt.Sprintf("r%d", e.revSeq),
		DiskSHA256:       sha,
		UnsavedDraft:     e.hasDraft,
	}
}

func (s *MemoryDocumentStore) ReadDocument(_ context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno) {
	if cleanRelPath(target.RelPath) == "" {
		return DocumentSnapshot{
			Target:  target,
			Mode:    fs.ModeDir | 0o755,
			ModTime: s.now().UTC(),
			IsDir:   true,
			Inode:   1,
			Revision: DocumentRevision{
				RootID: target.RootID,
				Branch: target.Branch,
			},
		}, pythonruntime.ErrnoSuccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := CanonicalKeyOf(target)
	e, ok := s.entries[k]
	if !ok {
		return DocumentSnapshot{
			Target:   target,
			Revision: s.currentRevisionLocked(k),
		}, pythonruntime.ErrnoENOENT
	}
	rev := s.revisionLocked(k, e)
	resolvedTarget := target
	resolvedTarget.DocumentID = e.docID
	var payload []byte
	if !e.isDir && !e.isSymlink {
		if e.hasDraft {
			payload = append([]byte(nil), e.draftBytes...)
		} else {
			payload = append([]byte(nil), e.savedBytes...)
		}
	}
	return DocumentSnapshot{
		Target:     resolvedTarget,
		Revision:   rev,
		Bytes:      payload,
		Mode:       e.mode,
		ModTime:    e.modTime,
		IsDir:      e.isDir,
		IsSymlink:  e.isSymlink,
		LinkTarget: e.linkTarget,
		Inode:      e.inode,
	}, pythonruntime.ErrnoSuccess
}

func (s *MemoryDocumentStore) StatDocument(ctx context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno) {
	if cleanRelPath(target.RelPath) == "" {
		return DocumentSnapshot{
			Target:  target,
			Mode:    fs.ModeDir | 0o755,
			ModTime: s.now().UTC(),
			IsDir:   true,
			Inode:   1,
		}, pythonruntime.ErrnoSuccess
	}
	return s.ReadDocument(ctx, target)
}

func (s *MemoryDocumentStore) ListDir(_ context.Context, target pythonruntime.CanonicalTarget) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dirRel := cleanRelPath(target.RelPath)
	if dirRel != "" {
		k := CanonicalKeyOf(target)
		e, ok := s.entries[k]
		if !ok {
			return nil, pythonruntime.ErrnoENOENT
		}
		if !e.isDir {
			return nil, pythonruntime.ErrnoENOTDIR
		}
	}
	seen := make(map[string]pythonruntime.DirEntry)
	for k, e := range s.entries {
		if k.Namespace != target.Namespace || k.RootID != target.RootID || k.Branch != target.Branch {
			continue
		}
		if k.RelPath == "" || k.RelPath == dirRel {
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
		seen[name] = pythonruntime.DirEntry{
			Name:      name,
			IsDir:     e.isDir,
			IsSymlink: e.isSymlink,
			Mode:      e.mode,
		}
	}
	out := make([]pythonruntime.DirEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, pythonruntime.ErrnoSuccess
}

func (s *MemoryDocumentStore) invokeBeforePublish(op string, target pythonruntime.CanonicalTarget) {
	s.mu.Lock()
	hook := s.BeforePublish
	s.mu.Unlock()
	if hook != nil {
		hook(op, target)
	}
}

func (s *MemoryDocumentStore) PublishWrite(_ context.Context, req PublishWriteRequest) (PublishResult, error) {
	op := "write"
	if req.Create {
		op = "create"
	}
	s.invokeBeforePublish(op, req.Target)

	s.mu.Lock()
	defer s.mu.Unlock()

	k := CanonicalKeyOf(req.Target)
	cur := s.currentRevisionLocked(k)
	if !req.BaseRevision.Matches(cur) {
		conflict := &pythonruntime.RevisionConflict{
			Target:           req.Target,
			ExpectedRevision: req.BaseRevision.Identity(),
			ActualRevision:   cur.Identity(),
		}
		s.publishLog = append(s.publishLog, MemoryPublishCall{
			Operation: op,
			Target:    req.Target,
			Bytes:     append([]byte(nil), req.Bytes...),
			Conflict:  true,
		})
		return PublishResult{Conflict: conflict}, &RevisionConflictError{Conflict: *conflict}
	}

	s.ensureParentDirsLocked(k.RootID, k.Branch, k.RelPath)
	e, exists := s.entries[k]
	if !exists || e.isDir || e.isSymlink {
		mode := req.Mode
		if mode == 0 {
			mode = 0o644
		}
		e = &memoryEntry{
			docID: s.allocDocID(),
			mode:  mode,
			inode: s.allocInode(),
		}
		s.entries[k] = e
	}
	delete(s.tombstoneRev, k)
	e.savedBytes = append([]byte(nil), req.Bytes...)
	e.hasDraft = false
	e.draftBytes = nil
	if req.Mode != 0 {
		e.mode = req.Mode
	}
	e.modTime = s.now().UTC()
	e.revSeq = s.allocRev()
	committed := s.revisionLocked(k, e)
	s.publishLog = append(s.publishLog, MemoryPublishCall{
		Operation: op,
		Target:    req.Target,
		Bytes:     append([]byte(nil), req.Bytes...),
	})
	return PublishResult{
		CommittedRevision: committed,
		BytesWritten:      int64(len(req.Bytes)),
	}, nil
}

func (s *MemoryDocumentStore) PublishDelete(_ context.Context, req PublishDeleteRequest) (PublishResult, error) {
	s.invokeBeforePublish("delete", req.Target)

	s.mu.Lock()
	defer s.mu.Unlock()

	k := CanonicalKeyOf(req.Target)
	cur := s.currentRevisionLocked(k)
	if !req.BaseRevision.Matches(cur) {
		conflict := &pythonruntime.RevisionConflict{
			Target:           req.Target,
			ExpectedRevision: req.BaseRevision.Identity(),
			ActualRevision:   cur.Identity(),
		}
		s.publishLog = append(s.publishLog, MemoryPublishCall{
			Operation: "delete",
			Target:    req.Target,
			Conflict:  true,
		})
		return PublishResult{Conflict: conflict}, &RevisionConflictError{Conflict: *conflict}
	}

	delete(s.entries, k)
	revToken := fmt.Sprintf("r%d", s.allocRev())
	s.tombstoneRev[k] = revToken
	s.publishLog = append(s.publishLog, MemoryPublishCall{
		Operation: "delete",
		Target:    req.Target,
	})
	return PublishResult{
		CommittedRevision: DocumentRevision{
			RootID:           k.RootID,
			Branch:           k.Branch,
			DocumentRevision: revToken,
			Absent:           true,
		},
	}, nil
}

func (s *MemoryDocumentStore) PublishMove(_ context.Context, req PublishMoveRequest) (PublishResult, error) {
	s.invokeBeforePublish("move", req.Target)

	s.mu.Lock()
	defer s.mu.Unlock()

	srcKey := CanonicalKeyOf(req.Source)
	dstKey := CanonicalKeyOf(req.Target)

	curSrc := s.currentRevisionLocked(srcKey)
	if !req.SourceBaseRevision.Matches(curSrc) {
		conflict := &pythonruntime.RevisionConflict{
			Target:           req.Source,
			ExpectedRevision: req.SourceBaseRevision.Identity(),
			ActualRevision:   curSrc.Identity(),
		}
		srcCopy := req.Source
		s.publishLog = append(s.publishLog, MemoryPublishCall{
			Operation: "move",
			Target:    req.Target,
			Source:    &srcCopy,
			Conflict:  true,
		})
		return PublishResult{Conflict: conflict}, &RevisionConflictError{Conflict: *conflict}
	}

	curDst := s.currentRevisionLocked(dstKey)
	if !req.TargetBaseRevision.Matches(curDst) {
		conflict := &pythonruntime.RevisionConflict{
			Target:           req.Target,
			ExpectedRevision: req.TargetBaseRevision.Identity(),
			ActualRevision:   curDst.Identity(),
		}
		srcCopy := req.Source
		s.publishLog = append(s.publishLog, MemoryPublishCall{
			Operation: "move",
			Target:    req.Target,
			Source:    &srcCopy,
			Conflict:  true,
		})
		return PublishResult{Conflict: conflict}, &RevisionConflictError{Conflict: *conflict}
	}

	srcEntry, ok := s.entries[srcKey]
	if !ok {
		return PublishResult{}, fmt.Errorf("move source %s does not exist", req.Source.RelPath)
	}
	delete(s.entries, srcKey)
	s.tombstoneRev[srcKey] = fmt.Sprintf("r%d", s.allocRev())

	s.ensureParentDirsLocked(dstKey.RootID, dstKey.Branch, dstKey.RelPath)
	moved := *srcEntry
	if req.HasModifiedBytes {
		moved.savedBytes = append([]byte(nil), req.Bytes...)
		moved.hasDraft = false
		moved.draftBytes = nil
	} else if moved.hasDraft {
		moved.savedBytes = append([]byte(nil), moved.draftBytes...)
		moved.hasDraft = false
		moved.draftBytes = nil
	}
	if req.Mode != 0 {
		moved.mode = req.Mode
	}
	moved.modTime = s.now().UTC()
	moved.revSeq = s.allocRev()
	s.entries[dstKey] = &moved
	delete(s.tombstoneRev, dstKey)

	committed := s.revisionLocked(dstKey, &moved)
	srcCopy := req.Source
	s.publishLog = append(s.publishLog, MemoryPublishCall{
		Operation: "move",
		Target:    req.Target,
		Source:    &srcCopy,
		Bytes:     append([]byte(nil), moved.savedBytes...),
	})
	return PublishResult{
		CommittedRevision: committed,
		BytesWritten:      int64(len(moved.savedBytes)),
	}, nil
}

func (s *MemoryDocumentStore) PublishMkdir(_ context.Context, req PublishMkdirRequest) (PublishResult, error) {
	s.invokeBeforePublish("mkdir", req.Target)

	s.mu.Lock()
	defer s.mu.Unlock()

	k := CanonicalKeyOf(req.Target)
	cur := s.currentRevisionLocked(k)
	if !req.BaseRevision.Matches(cur) {
		conflict := &pythonruntime.RevisionConflict{
			Target:           req.Target,
			ExpectedRevision: req.BaseRevision.Identity(),
			ActualRevision:   cur.Identity(),
		}
		s.publishLog = append(s.publishLog, MemoryPublishCall{
			Operation: "mkdir",
			Target:    req.Target,
			Conflict:  true,
		})
		return PublishResult{Conflict: conflict}, &RevisionConflictError{Conflict: *conflict}
	}

	s.ensureParentDirsLocked(k.RootID, k.Branch, k.RelPath)
	mode := req.Mode
	if mode == 0 {
		mode = 0o755
	}
	e := &memoryEntry{
		docID:   s.allocDocID(),
		revSeq:  s.allocRev(),
		mode:    mode | fs.ModeDir,
		modTime: s.now().UTC(),
		isDir:   true,
		inode:   s.allocInode(),
	}
	s.entries[k] = e
	delete(s.tombstoneRev, k)
	s.publishLog = append(s.publishLog, MemoryPublishCall{
		Operation: "mkdir",
		Target:    req.Target,
	})
	return PublishResult{
		CommittedRevision: s.revisionLocked(k, e),
	}, nil
}

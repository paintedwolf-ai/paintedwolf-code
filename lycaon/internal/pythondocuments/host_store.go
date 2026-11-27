package pythondocuments

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// ErrMutationDoorUnavailable is returned when HostDocumentStore is asked to
// publish a workspace mutation without an injected apply-and-record
// MutationDoor.
var ErrMutationDoorUnavailable = errors.New("host mutation door not configured")

// EditorDraft represents an open editor document's live state captured from
// Painted Wolf's editor subsystem.
type EditorDraft struct {
	DocumentID string
	Revision   string
	Bytes      []byte
	BaseSHA256 string
	Dirty      bool
	Absent     bool
}

// EditorBridge connects HostDocumentStore to Painted Wolf's live editor
// document service (e.g. tools.EditorDocuments) for unsaved draft capture and
// revision-checked publication. Any error returned by CaptureDraft MUST
// propagate rather than falling back to saved disk bytes.
type EditorBridge interface {
	CaptureDraft(ctx context.Context, rootID, branch, relPath string) (EditorDraft, bool, error)
	PublishDraft(ctx context.Context, rootID, branch, relPath string, baseRev DocumentRevision, data []byte) (DocumentRevision, bool, error)
}

// MutationDoor adapts HostDocumentStore publication onto Painted Wolf's single
// apply-and-record source mutation door (tools/native/source_write.go:
// applyAgentFile, removeAgentPath, renameAgentEntry, mkdirAgentPath).
// HostDocumentStore never mutates workspace files directly outside this door.
type MutationDoor interface {
	ApplyFile(ctx context.Context, req PublishWriteRequest) (PublishResult, error)
	RemovePath(ctx context.Context, req PublishDeleteRequest) (PublishResult, error)
	RenamePath(ctx context.Context, req PublishMoveRequest) (PublishResult, error)
	MkdirPath(ctx context.Context, req PublishMkdirRequest) (PublishResult, error)
}

// HostRootConfig maps one logical RootID/Branch to a physical directory on the
// host filesystem.
type HostRootConfig struct {
	ID       string
	Branch   string
	Dir      string
	ReadOnly bool
}

// HostDocumentStore is the production DocumentStore backed by real workspace
// root directories for reads, an optional EditorBridge for unsaved editor
// drafts, and an injected MutationDoor for all workspace publications.
type HostDocumentStore struct {
	mu     sync.RWMutex
	roots  map[hostRootKey]HostRootConfig
	editor EditorBridge
	door   MutationDoor
}

type hostRootKey struct {
	rootID string
	branch string
}

var _ DocumentStore = (*HostDocumentStore)(nil)

// NewHostDocumentStore constructs a production HostDocumentStore over the
// supplied physical roots, optional EditorBridge, and apply-and-record
// MutationDoor.
func NewHostDocumentStore(roots []HostRootConfig, editor EditorBridge, door MutationDoor) *HostDocumentStore {
	m := make(map[hostRootKey]HostRootConfig, len(roots))
	for _, r := range roots {
		r.Dir = filepath.Clean(r.Dir)
		m[hostRootKey{rootID: r.ID, branch: r.Branch}] = r
	}
	return &HostDocumentStore{
		roots:  m,
		editor: editor,
		door:   door,
	}
}

func (s *HostDocumentStore) rootFor(target pythonruntime.CanonicalTarget) (HostRootConfig, pythonruntime.Errno) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roots[hostRootKey{rootID: target.RootID, branch: target.Branch}]
	if !ok {
		return HostRootConfig{}, pythonruntime.ErrnoENOENT
	}
	return r, pythonruntime.ErrnoSuccess
}

func (s *HostDocumentStore) ReadDocument(ctx context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno) {
	root, errno := s.rootFor(target)
	if errno != pythonruntime.ErrnoSuccess {
		return DocumentSnapshot{Target: target}, errno
	}
	rel := cleanRelPath(target.RelPath)

	abs := filepath.Join(root.Dir, filepath.FromSlash(rel))
	linfo, err := os.Lstat(abs)
	if err == nil && linfo.IsDir() {
		return DocumentSnapshot{
			Target:  target,
			Mode:    linfo.Mode(),
			ModTime: linfo.ModTime().UTC(),
			IsDir:   true,
			Revision: DocumentRevision{
				RootID: target.RootID,
				Branch: target.Branch,
			},
		}, pythonruntime.ErrnoSuccess
	}
	if err == nil && linfo.Mode()&os.ModeSymlink != 0 {
		linkTarget, lerr := os.Readlink(abs)
		if lerr != nil {
			return DocumentSnapshot{Target: target}, mapOSErrno(lerr)
		}
		return DocumentSnapshot{
			Target:     target,
			Mode:       linfo.Mode(),
			ModTime:    linfo.ModTime().UTC(),
			IsSymlink:  true,
			LinkTarget: linkTarget,
			Revision: DocumentRevision{
				RootID:     target.RootID,
				Branch:     target.Branch,
				DiskSHA256: SHA256Hex([]byte(linkTarget)),
			},
		}, pythonruntime.ErrnoSuccess
	}

	if s.editor != nil && rel != "" {
		draft, ok, err := s.editor.CaptureDraft(ctx, target.RootID, target.Branch, rel)
		if err != nil {
			// Never silently fall back to saved disk bytes when editor capture
			// fails; doing so could select a stale or wrong revision.
			return DocumentSnapshot{Target: target}, mapOSErrno(err)
		}
		if ok {
			sha := SHA256Hex(draft.Bytes)
			resolved := target
			resolved.DocumentID = draft.DocumentID
			rev := DocumentRevision{
				RootID:           target.RootID,
				Branch:           target.Branch,
				DocumentID:       draft.DocumentID,
				DocumentRevision: draft.Revision,
				DiskSHA256:       sha,
				UnsavedDraft:     draft.Dirty,
			}
			var mode fs.FileMode = 0o644
			if linfo != nil {
				mode = linfo.Mode()
			}
			return DocumentSnapshot{
				Target:   resolved,
				Revision: rev,
				Bytes:    append([]byte(nil), draft.Bytes...),
				Mode:     mode,
			}, pythonruntime.ErrnoSuccess
		}
	}

	if err != nil {
		if os.IsNotExist(err) {
			return DocumentSnapshot{
				Target:   target,
				Revision: DocumentRevision{RootID: target.RootID, Branch: target.Branch, Absent: true},
			}, pythonruntime.ErrnoENOENT
		}
		return DocumentSnapshot{Target: target}, mapOSErrno(err)
	}
	f, err := fseffect.OpenRead(fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(rel)})
	if err != nil {
		return DocumentSnapshot{Target: target}, mapOSErrno(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return DocumentSnapshot{Target: target}, mapOSErrno(err)
	}
	sha := SHA256Hex(data)
	return DocumentSnapshot{
		Target:  target,
		Bytes:   data,
		Mode:    linfo.Mode(),
		ModTime: linfo.ModTime().UTC(),
		Revision: DocumentRevision{
			RootID:           target.RootID,
			Branch:           target.Branch,
			DocumentRevision: "disk:" + sha,
			DiskSHA256:       sha,
		},
	}, pythonruntime.ErrnoSuccess
}

func (s *HostDocumentStore) StatDocument(ctx context.Context, target pythonruntime.CanonicalTarget) (DocumentSnapshot, pythonruntime.Errno) {
	root, errno := s.rootFor(target)
	if errno != pythonruntime.ErrnoSuccess {
		return DocumentSnapshot{Target: target}, errno
	}
	rel := cleanRelPath(target.RelPath)
	if rel == "" {
		info, err := os.Stat(root.Dir)
		if err != nil {
			return DocumentSnapshot{Target: target}, mapOSErrno(err)
		}
		return DocumentSnapshot{
			Target:  target,
			Mode:    info.Mode(),
			ModTime: info.ModTime().UTC(),
			IsDir:   true,
		}, pythonruntime.ErrnoSuccess
	}
	return s.ReadDocument(ctx, target)
}

func (s *HostDocumentStore) ListDir(_ context.Context, target pythonruntime.CanonicalTarget) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	root, errno := s.rootFor(target)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	abs := filepath.Join(root.Dir, filepath.FromSlash(cleanRelPath(target.RelPath)))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, mapOSErrno(err)
	}
	out := make([]pythonruntime.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		var mode fs.FileMode
		if ierr == nil {
			mode = info.Mode()
		}
		out = append(out, pythonruntime.DirEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			IsSymlink: mode&os.ModeSymlink != 0,
			Mode:      mode,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, pythonruntime.ErrnoSuccess
}

func (s *HostDocumentStore) PublishWrite(ctx context.Context, req PublishWriteRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	rel := cleanRelPath(req.Target.RelPath)
	if s.editor != nil {
		rev, handled, err := s.editor.PublishDraft(ctx, req.Target.RootID, req.Target.Branch, rel, req.BaseRevision, req.Bytes)
		if err != nil {
			var conflictErr *RevisionConflictError
			if errors.As(err, &conflictErr) {
				c := conflictErr.Conflict
				return PublishResult{Conflict: &c}, err
			}
			return PublishResult{}, err
		}
		if handled {
			return PublishResult{
				CommittedRevision: rev,
				BytesWritten:      int64(len(req.Bytes)),
			}, nil
		}
	}
	if s.door == nil {
		return PublishResult{}, ErrMutationDoorUnavailable
	}
	return s.door.ApplyFile(ctx, req)
}

func (s *HostDocumentStore) PublishDelete(ctx context.Context, req PublishDeleteRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	if s.door == nil {
		return PublishResult{}, ErrMutationDoorUnavailable
	}
	return s.door.RemovePath(ctx, req)
}

func (s *HostDocumentStore) PublishMove(ctx context.Context, req PublishMoveRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	if s.door == nil {
		return PublishResult{}, ErrMutationDoorUnavailable
	}
	return s.door.RenamePath(ctx, req)
}

func (s *HostDocumentStore) PublishMkdir(ctx context.Context, req PublishMkdirRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	if s.door == nil {
		return PublishResult{}, ErrMutationDoorUnavailable
	}
	return s.door.MkdirPath(ctx, req)
}

func mapOSErrno(err error) pythonruntime.Errno {
	switch {
	case err == nil:
		return pythonruntime.ErrnoSuccess
	case os.IsNotExist(err):
		return pythonruntime.ErrnoENOENT
	case os.IsExist(err):
		return pythonruntime.ErrnoEEXIST
	case os.IsPermission(err), errors.Is(err, fseffect.ErrSymlink), errors.Is(err, fseffect.ErrInvalidPath):
		return pythonruntime.ErrnoEACCES
	case strings.Contains(err.Error(), "not a directory"):
		return pythonruntime.ErrnoENOTDIR
	case strings.Contains(err.Error(), "is a directory"):
		return pythonruntime.ErrnoEISDIR
	default:
		return pythonruntime.ErrnoEIO
	}
}

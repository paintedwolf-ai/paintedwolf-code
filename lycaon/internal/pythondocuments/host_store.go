package pythondocuments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

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
// revision-checked publication.
type EditorBridge interface {
	CaptureDraft(ctx context.Context, rootID, branch, relPath string) (EditorDraft, bool, error)
	PublishDraft(ctx context.Context, rootID, branch, relPath string, baseRev DocumentRevision, data []byte) (DocumentRevision, bool, error)
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
// root directories, Painted Wolf's fseffect atomic write door, and an optional
// EditorBridge for unsaved editor drafts.
type HostDocumentStore struct {
	mu     sync.RWMutex
	roots  map[hostRootKey]HostRootConfig
	editor EditorBridge
}

type hostRootKey struct {
	rootID string
	branch string
}

var _ DocumentStore = (*HostDocumentStore)(nil)

// NewHostDocumentStore constructs a production HostDocumentStore over the
// supplied physical roots and optional EditorBridge.
func NewHostDocumentStore(roots []HostRootConfig, editor EditorBridge) *HostDocumentStore {
	m := make(map[hostRootKey]HostRootConfig, len(roots))
	for _, r := range roots {
		r.Dir = filepath.Clean(r.Dir)
		m[hostRootKey{rootID: r.ID, branch: r.Branch}] = r
	}
	return &HostDocumentStore{
		roots:  m,
		editor: editor,
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
	if s.editor != nil && rel != "" {
		if draft, ok, err := s.editor.CaptureDraft(ctx, target.RootID, target.Branch, rel); err == nil && ok {
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
			if info, statErr := os.Stat(filepath.Join(root.Dir, filepath.FromSlash(rel))); statErr == nil {
				mode = info.Mode()
			}
			return DocumentSnapshot{
				Target:   resolved,
				Revision: rev,
				Bytes:    append([]byte(nil), draft.Bytes...),
				Mode:     mode,
			}, pythonruntime.ErrnoSuccess
		}
	}

	abs := filepath.Join(root.Dir, filepath.FromSlash(rel))
	linfo, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return DocumentSnapshot{
				Target:   target,
				Revision: DocumentRevision{RootID: target.RootID, Branch: target.Branch, Absent: true},
			}, pythonruntime.ErrnoENOENT
		}
		return DocumentSnapshot{Target: target}, mapOSErrno(err)
	}
	if linfo.IsDir() {
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
	if linfo.Mode()&os.ModeSymlink != 0 {
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

	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(rel)}
	var conflict *pythonruntime.RevisionConflict
	res, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:     loc,
		Source:       bytes.NewReader(req.Bytes),
		Mode:         mode,
		PreserveMode: !req.Create,
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			if c := checkDiskBasePrecondition(current, req.Target, req.BaseRevision); c != nil {
				conflict = c
				return &RevisionConflictError{Conflict: *c}
			}
			return nil
		},
	})
	if err != nil {
		return PublishResult{Conflict: conflict}, err
	}
	committed := DocumentRevision{
		RootID:           req.Target.RootID,
		Branch:           req.Target.Branch,
		DocumentRevision: "disk:" + res.SHA256,
		DiskSHA256:       res.SHA256,
	}
	return PublishResult{
		CommittedRevision: committed,
		BytesWritten:      res.Bytes,
	}, nil
}

func (s *HostDocumentStore) PublishDelete(_ context.Context, req PublishDeleteRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	rel := cleanRelPath(req.Target.RelPath)
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(rel)}
	var conflict *pythonruntime.RevisionConflict
	err := fseffect.Remove(fseffect.RemoveRequest{
		Location: loc,
		BeforeCommit: func(current fseffect.Target) error {
			if c := checkDiskBasePrecondition(current, req.Target, req.BaseRevision); c != nil {
				conflict = c
				return &RevisionConflictError{Conflict: *c}
			}
			return nil
		},
	})
	if err != nil {
		return PublishResult{Conflict: conflict}, err
	}
	return PublishResult{
		CommittedRevision: DocumentRevision{
			RootID: req.Target.RootID,
			Branch: req.Target.Branch,
			Absent: true,
		},
	}, nil
}

func (s *HostDocumentStore) PublishMove(ctx context.Context, req PublishMoveRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	srcRel := cleanRelPath(req.Source.RelPath)
	dstRel := cleanRelPath(req.Target.RelPath)
	srcAbs := filepath.Join(root.Dir, filepath.FromSlash(srcRel))
	srcLoc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(srcRel)}
	dstLoc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(dstRel)}

	if req.HasModifiedBytes {
		writeRes, err := s.PublishWrite(ctx, PublishWriteRequest{
			Target:       req.Target,
			BaseRevision: req.TargetBaseRevision,
			Bytes:        req.Bytes,
			Mode:         req.Mode,
			Create:       req.TargetBaseRevision.Absent,
		})
		if err != nil {
			return writeRes, err
		}
		if _, err := s.PublishDelete(ctx, PublishDeleteRequest{
			Target:       req.Source,
			BaseRevision: req.SourceBaseRevision,
		}); err != nil {
			return PublishResult{}, err
		}
		return writeRes, nil
	}

	identity, err := fspath.EntryIdentity(srcAbs)
	if err != nil {
		return PublishResult{}, err
	}
	if req.TargetBaseRevision.Absent {
		err = fseffect.RelocateGuarded(srcLoc, dstLoc, identity)
	} else {
		err = fseffect.ReplaceByRelocateGuarded(srcLoc, dstLoc, identity)
	}
	if err != nil {
		return PublishResult{}, err
	}
	snap, stErrno := s.ReadDocument(ctx, req.Target)
	if stErrno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, stErrno
	}
	return PublishResult{
		CommittedRevision: snap.Revision,
		BytesWritten:      int64(len(snap.Bytes)),
	}, nil
}

func (s *HostDocumentStore) PublishMkdir(_ context.Context, req PublishMkdirRequest) (PublishResult, error) {
	root, errno := s.rootFor(req.Target)
	if errno != pythonruntime.ErrnoSuccess {
		return PublishResult{}, errno
	}
	if root.ReadOnly {
		return PublishResult{}, pythonruntime.ErrnoEROFS
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o755
	}
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(cleanRelPath(req.Target.RelPath))}
	if _, err := fseffect.MkdirAll(loc, mode); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{
		CommittedRevision: DocumentRevision{
			RootID:           req.Target.RootID,
			Branch:           req.Target.Branch,
			DocumentRevision: "dir:created",
		},
	}, nil
}

func checkDiskBasePrecondition(current fseffect.Target, target pythonruntime.CanonicalTarget, base DocumentRevision) *pythonruntime.RevisionConflict {
	info, err := current.Lstat()
	if os.IsNotExist(err) {
		if base.Absent {
			return nil
		}
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "absent",
		}
	}
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("stat-error:%v", err),
		}
	}
	if base.Absent {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "exists",
		}
	}
	if info.IsDir() || base.DiskSHA256 == "" {
		return nil
	}
	f, err := current.Open()
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("open-error:%v", err),
		}
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("read-error:%v", err),
		}
	}
	actualSHA := SHA256Hex(data)
	if actualSHA != base.DiskSHA256 {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "rev=disk:" + actualSHA + "|sha256=" + actualSHA,
		}
	}
	return nil
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

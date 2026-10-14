package editordoc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

func (o documentOpener) projectBranch(ctx context.Context, projectID string, branch sourcebranch.ID) (*project.Project, error) {
	s := o.s
	if s.roots == nil {
		return nil, ErrNotFound
	}
	p, err := s.roots.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return project.ResolveBranch(ctx, s.store.db, p, branch)
}

func checkWorkspace(p *project.Project, d *Document) error {
	if p == nil || p.ID != d.ProjectID || p.BranchForRoot(d.RootID) != d.BranchID {
		return ErrNotFound
	}
	return nil
}

// Workspace resolves an addressed document independently of the focused chat.
func (l *LifecycleService) Workspace(ctx context.Context, p *project.Project, id string) (*project.Project, error) {
	d, err := l.s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	if p.BranchForRoot(d.RootID) == d.BranchID {
		return p, nil
	}
	return l.s.projectBranch(ctx, p.ID, d.BranchID)
}

func (o documentOpener) projectWorkspacePresentation(ctx context.Context, d *Document) {
	if p, err := o.s.projectBranch(ctx, d.ProjectID, d.BranchID); err == nil {
		d.WorkspaceID = p.WorkspaceID()
	}
}

func (o documentOpener) openAbsentDocument(ctx context.Context, p *project.Project, path, rootID, clientID string, options documentOpenOptions) (*Document, error) {
	s := o.s
	rel, err := projectsource.ResolveAbsentSourcePath(p, rootID, path)
	if err != nil {
		return nil, err
	}
	branch := p.BranchForRoot(rootID)
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlockIdentity := s.docLocks.lock(identityLockKey(p.ID+"\x00"+branch.String(), rootID, rel))
	defer unlockIdentity()
	d, err := s.store.GetByIdentity(ctx, p.ID, branch, rootID, rel)
	if errors.Is(err, ErrNotFound) {
		return nil, projectsource.ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	unlockDoc := s.docLocks.lock(documentLockKey(d.ID))
	defer unlockDoc()
	if err := s.markAbsent(ctx, d); err != nil {
		return nil, err
	}
	return s.admitDocument(ctx, d, clientID, options)
}

func (o documentOpener) openObservedDocument(ctx context.Context, p *project.Project, observation *projectsource.SourceReadObservation, read *projectsource.SourceReadResult, decodeAs, clientID string, options documentOpenOptions) (*Document, error) {
	s := o.s
	branch := p.BranchForRoot(read.RootID)
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlockIdentity := s.docLocks.lock(identityLockKey(p.ID+"\x00"+branch.String(), read.RootID, read.Path))
	defer unlockIdentity()
	d, err := s.store.GetByIdentity(ctx, p.ID, branch, read.RootID, read.Path)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if d != nil {
		unlockDoc := s.docLocks.lock(documentLockKey(d.ID))
		defer unlockDoc()
		observation, err = projectsource.ObserveProjectSource(p, projectsource.SourceReadRequest{Path: read.Path, RootID: read.RootID, DecodeAs: decodeAs})
		if errors.Is(err, projectsource.ErrSourceNotFound) {
			if err := s.markAbsent(ctx, d); err != nil {
				return nil, err
			}
			return s.admitDocument(ctx, d, clientID, options)
		}
		if err != nil {
			return nil, err
		}
		latest, err := observation.Project()
		if err != nil {
			return nil, err
		}
		*read = *latest
		if !options.requireWritable {
			if err := validateDocumentAdmission(read); err != nil {
				return nil, err
			}
		}
	}
	tracked, err := s.ledger.TrackFile(ctx, trackInput(p, observation))
	if err != nil {
		return nil, err
	}
	read.FileID, read.VersionID = tracked.FileID, tracked.VersionID
	if options.requireWritable && (!read.Writable || read.Binary || read.OverLimit || read.Encoding == "") {
		return nil, nil
	}
	if d == nil {
		content, eol, mixed := normalizeEOL(read.Content)
		now := time.Now().UTC()
		d = &Document{ID: uuid.NewString(), ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), BranchID: branch, FileID: tracked.FileID,
			RootID: read.RootID, Path: read.Path,
			Draft: content, BaseContent: content, BaseSHA256: read.SHA256, Encoding: read.Encoding,
			SizeBytes: read.SizeBytes,
			EOL:       eol, BaseEOL: eol, MixedEOL: mixed, BaseMixedEOL: mixed,
			Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.store.Insert(ctx, d); err != nil {
			return nil, err
		}
	} else if err := s.reconcileObservedDocument(ctx, d, read, false, "", tracked.FileID); err != nil {
		return nil, err
	} else if err := s.store.MarkOpened(ctx, d.ID, time.Now()); err != nil {
		return nil, err
	}
	return s.admitDocument(ctx, d, clientID, options)
}

func trackInput(p *project.Project, observation *projectsource.SourceReadObservation) sourceledger.TrackInput {
	return sourceledger.TrackInput{
		ProjectID: p.ID, BranchID: p.BranchForRoot(observation.RootID),
		RootID: observation.RootID, Path: observation.Path, EntryKind: sourceledger.EntryKindFile,
		SHA256: observation.Revision.SHA256, Content: observation.Revision.Bytes, Size: observation.SizeBytes,
	}
}

func (o documentOpener) admitDocument(ctx context.Context, d *Document, clientID string, options documentOpenOptions) (*Document, error) {
	s := o.s
	if err := s.projectReplica(ctx, d, options.projectionVector(d)); err != nil {
		return nil, err
	}
	viewer, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return nil, err
	}
	if err := s.bindClientPerson(viewer.clientID, viewer.personID); err != nil {
		return nil, err
	}
	if viewer.clientID != "" {
		if _, err := s.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO editor_document_retention(document_id,client_id,person_id) VALUES(?,?,?)`, d.ID, viewer.clientID, viewer.personID); err != nil {
			return nil, err
		}
	}
	if options.join {
		s.joinParticipant(ctx, d.ID, clientID, "")
	}
	return s.withParticipants(d), nil
}

func readDocumentSource(p *project.Project, d *Document) (*projectsource.SourceReadResult, error) {
	_, read, err := observeDocumentSource(p, d)
	return read, err
}

func observeDocumentSource(p *project.Project, d *Document) (*projectsource.SourceReadObservation, *projectsource.SourceReadResult, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, nil, err
	}
	req := projectsource.SourceReadRequest{Path: d.Path, RootID: d.RootID}
	observation, err := projectsource.ObserveProjectSource(p, req)
	if err != nil {
		return nil, nil, err
	}
	read, err := observation.Project()
	decodeAs := documentDecodeAs(d)
	if decodeAs == "" {
		return observation, read, err
	}
	var unsupported *projectsource.SourceUnsupportedEncodingError
	if err != nil && !errors.As(err, &unsupported) {
		return observation, nil, err
	}
	if err == nil && (!read.Binary || read.OverLimit || strings.HasPrefix(read.MIME, "image/")) {
		return observation, read, nil
	}
	req.DecodeAs = decodeAs
	observation, err = projectsource.ObserveProjectSource(p, req)
	if err != nil {
		return nil, nil, err
	}
	read, err = observation.Project()
	return observation, read, err
}

func documentDecodeAs(d *Document) string {
	switch d.Encoding {
	case textfile.UTF16LE, textfile.UTF16BE:
		return d.Encoding
	default:
		return ""
	}
}

func validateDocumentAdmission(read *projectsource.SourceReadResult) error {
	if err := validateEditableSource(read); err != nil {
		return err
	}
	if !read.Writable {
		return ErrReadOnly
	}
	return nil
}

func validateEditableSource(read *projectsource.SourceReadResult) error {
	if read.OverLimit {
		return projectsource.ErrSourceWriteTooLarge
	}
	if read.Binary || read.Encoding == "" {
		return projectsource.ErrSourceBinary
	}
	return nil
}


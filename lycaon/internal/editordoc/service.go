package editordoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

type Ledger interface {
	sourceledger.FileTracker
	QueryAttribution(context.Context, string, sourcebranch.ID, string, string) (sourceledger.AttributionResult, error)
	Prepare(context.Context, []sourceledger.RecordInput) (sourceledger.PreparedRecording, error)
	PrepareHeldEdit(context.Context, sourceledger.HeldEdit) (sourceledger.PreparedHeldEdit, error)
}

type Service struct {
	agentReads      agentReadCache
	replicas        replicaRuntime
	store           *Store
	ledger          Ledger
	sourceMutations *project.SourceMutationService
	// roots resolves stable root identities to live paths.
	roots RootSource
	// ops lets recovery and retargets quiesce document operations.
	ops sync.RWMutex
	// docLocks serializes each document independently.
	docLocks keyedLocks
	// presenceMu guards ephemeral participants.
	presenceMu     sync.Mutex
	presenceClosed bool
	participants   map[string]map[string]Participant
	windowNumbers  map[string]int
	// clientPeople binds each client to the one person using it.
	clientPeople map[string]string
	onChange     func(context.Context, Change)
}

func (s *Service) SetSourceMutations(mutations *project.SourceMutationService) {
	s.sourceMutations = mutations
}

func (s *Service) reserveDocumentSource(ctx context.Context, p *project.Project, d *Document) (func(), error) {
	var pending bool
	err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM editor_retargets r JOIN source_mutations m ON m.id=r.source_operation_id WHERE r.project_id=? AND r.branch_id=? AND r.root_id=? AND (?=r.from_path OR substr(?,1,length(r.from_path)+1)=r.from_path||'/') AND (m.status IN ('prepared','file_applied','committed') OR COALESCE(json_extract(m.plan_json,'$.hold_started'),0)=1))`, d.ProjectID, d.BranchID, d.RootID, d.Path, d.Path).Scan(&pending)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, project.ErrSourceBusy
	}
	return s.sourceMutations.ReserveSourcePath(rootPath(p, d.RootID), d.Path)
}

// Change describes the projection needed after one document transition.
type Change struct {
	Document       *Document
	ContentChanged bool
}

// keyedLocks hands out one mutex per live key.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// Distinct, sorted document IDs give transactions a consistent lock order.
func (s *Service) lockDocuments(ids []string) func() {
	release := make([]func(), 0, len(ids))
	for _, id := range ids {
		release = append(release, s.docLocks.lock(documentLockKey(id)))
	}
	return func() {
		for i := len(release) - 1; i >= 0; i-- {
			release[i]()
		}
	}
}

func documentLockKey(id string) string { return "doc\x00" + strings.TrimSpace(id) }

func identityLockKey(projectID, rootID, path string) string {
	return "path\x00" + projectID + "\x00" + rootID + "\x00" + path
}

// RootSource answers where a project's roots point right now.
type RootSource interface {
	Get(ctx context.Context, projectID string) (*project.Project, error)
}

func New(store *Store, ledger Ledger, roots RootSource) *Service {
	if ledger == nil {
		panic("editor document ledger is required")
	}
	return &Service{store: store, ledger: ledger, roots: roots,
		windowNumbers: make(map[string]int), clientPeople: make(map[string]string),
		participants: make(map[string]map[string]Participant)}
}

// liveRootPath resolves a document root to its current path.
func (s *Service) liveRootPath(ctx context.Context, projectID string, branch sourcebranch.ID, rootID string) (string, error) {
	if s.roots == nil {
		return "", ErrNotFound
	}
	p, err := s.projectBranch(ctx, projectID, branch)
	if err != nil {
		return "", err
	}
	path := rootPath(p, rootID)
	if path == "" {
		return "", ErrRootDetached
	}
	return path, nil
}

func (s *Service) SetOnChange(fn func(context.Context, Change)) { s.onChange = fn }

// ForgetRemoved drops presence for deleted documents.
func (s *Service) ForgetRemoved(documentIDs []string) {
	if s == nil || len(documentIDs) == 0 {
		return
	}
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	for _, id := range documentIDs {
		for _, participant := range s.participants[strings.TrimSpace(id)] {
			if participant.expiry != nil {
				participant.expiry.Stop()
			}
		}
		delete(s.participants, strings.TrimSpace(id))
	}
}

// CurrentRevision returns tracked state without opening a document.
func (s *Service) CurrentRevision(ctx context.Context, p *project.Project, rootID, path string) (int64, bool, error) {
	if s == nil || p == nil {
		return 0, false, nil
	}
	d, err := s.store.GetByIdentity(ctx, p.ID, p.BranchForRoot(rootID), rootID, path)
	if errors.Is(err, ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return d.Revision, true, nil
}

// Snapshot returns an exact persisted revision without changing document state.
func (s *Service) Snapshot(ctx context.Context, projectID, documentID string, expectedRevision int64) (*Document, error) {
	if expectedRevision < 1 {
		return nil, ErrRevisionConflict
	}
	return s.Pin(ctx, projectID, documentID, expectedRevision)
}

// CurrentSnapshot returns the latest persisted document without changing it.
func (s *Service) CurrentSnapshot(ctx context.Context, projectID, documentID string) (*Document, error) {
	if s == nil || strings.TrimSpace(documentID) == "" {
		return nil, ErrNotFound
	}
	d, err := s.store.Get(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if d.ProjectID != strings.TrimSpace(projectID) {
		return nil, ErrNotFound
	}
	copy := *d
	s.projectWorkspacePresentation(ctx, &copy)
	return s.withParticipants(&copy), nil
}

// Open admits an editable document with optional client replica state;
// source viewers use OpenObserved for all file kinds. A deleted path opens
// its document in the absent state.
func (s *Service) Open(ctx context.Context, p *project.Project, path, rootID, decodeAs, clientID string, retained *Retained) (*Document, error) {
	options := documentOpenOptions{join: true, retained: retained}
	for attempt := 0; ; attempt++ {
		observation, err := project.ObserveProjectSource(p, project.SourceReadRequest{Path: path, RootID: rootID, DecodeAs: decodeAs})
		if errors.Is(err, project.ErrSourceNotFound) {
			d, absentErr := s.openAbsentDocument(ctx, p, path, rootID, clientID, options)
			// The file came back between the two looks; read it instead.
			if errors.Is(absentErr, project.ErrSourceExists) && attempt == 0 {
				continue
			}
			return d, absentErr
		}
		if err != nil {
			return nil, err
		}
		read, err := observation.Project()
		if err != nil {
			return nil, err
		}
		if err := validateDocumentAdmission(read); err != nil {
			return nil, err
		}
		return s.openObservedDocument(ctx, p, observation, read, decodeAs, clientID, options)
	}
}

// openAbsentDocument resumes the document a deleted path still has. A path
// without a document is simply not found.
func (s *Service) openAbsentDocument(ctx context.Context, p *project.Project, path, rootID, clientID string, options documentOpenOptions) (*Document, error) {
	rel, err := project.ResolveAbsentSourcePath(p, rootID, path)
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
		return nil, project.ErrSourceNotFound
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

// OpenedSource pairs the saved observation with the authoritative live draft.
type OpenedSource struct {
	Source   *project.SourceReadResult
	Document *Document
}

// Retained identifies a replica the client already holds for the document it expects to open.
type Retained struct {
	DocumentID string
	Epoch      int64
	Vector     []byte
}

// OpenObserved projects an observed source and opens its live document when the text is editable.
func (s *Service) OpenObserved(ctx context.Context, p *project.Project, observation *project.SourceReadObservation, decodeAs, clientID string, retained *Retained) (*OpenedSource, error) {
	read, err := observation.Project()
	if err != nil {
		return nil, err
	}
	opened := &OpenedSource{Source: read}
	if read.OverLimit || read.Binary || read.Encoding == "" || !read.Writable {
		return opened, nil
	}
	opened.Document, err = s.openObservedDocument(ctx, p, observation, read, decodeAs, clientID, documentOpenOptions{requireWritable: true, retained: retained})
	return opened, err
}

type documentOpenOptions struct {
	requireWritable bool
	join            bool
	// A matching retained identity turns the projection into a differential update.
	retained *Retained
}

func (o documentOpenOptions) projectionVector(d *Document) func(head *ReplicaHead) []byte {
	return func(head *ReplicaHead) []byte {
		if o.retained == nil || o.retained.DocumentID != d.ID || o.retained.Epoch != head.Epoch {
			return nil
		}
		return o.retained.Vector
	}
}

func (s *Service) openObservedDocument(ctx context.Context, p *project.Project, observation *project.SourceReadObservation, read *project.SourceReadResult, decodeAs, clientID string, options documentOpenOptions) (*Document, error) {
	// Path identity prevents duplicate documents before the document lock exists.
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
		// Observed again under the document lock, after the latest transition.
		unlockDoc := s.docLocks.lock(documentLockKey(d.ID))
		defer unlockDoc()
		observation, err = project.ObserveProjectSource(p, project.SourceReadRequest{Path: read.Path, RootID: read.RootID, DecodeAs: decodeAs})
		if errors.Is(err, project.ErrSourceNotFound) {
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

// trackInput describes an observed file to the ledger the way an open does.
func trackInput(p *project.Project, observation *project.SourceReadObservation) sourceledger.TrackInput {
	return sourceledger.TrackInput{
		ProjectID: p.ID, BranchID: p.BranchForRoot(observation.RootID),
		RootID: observation.RootID, Path: observation.Path, EntryKind: sourceledger.EntryKindFile,
		SHA256: observation.Revision.SHA256, Content: observation.Revision.Bytes, Size: observation.SizeBytes,
	}
}

// admitDocument projects the replica for the client and records its hold.
// The caller holds the document lock.
func (s *Service) admitDocument(ctx context.Context, d *Document, clientID string, options documentOpenOptions) (*Document, error) {
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

// ObserveDisk reconciles through the same transition as filesystem observation.
func (s *Service) ObserveDisk(ctx context.Context, p *project.Project, id, clientID string) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileDocumentDisk(ctx, p, d, clientID); err != nil {
		return nil, err
	}
	if err := s.replicaProjection(ctx, d, nil); err != nil {
		return nil, err
	}
	return s.withParticipants(d), nil
}

// Save publishes an exact accepted snapshot and deduplicates operation retries.
func (s *Service) Save(ctx context.Context, p *project.Project, id, clientID, operationID, sessionID string, turn int, expected int64) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	saver, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return nil, err
	}
	actor := saveActor{Origin: api.SourceChangeOriginUser, PersonID: saver.personID, ClientID: saver.clientID,
		SessionID: strings.TrimSpace(sessionID), Turn: turn}
	replay, err := s.findSaveReplay(ctx, p, id, operationID, actor, expected)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return s.resumeSave(ctx, p, replay)
	}
	current, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	reserved, err := s.store.savePinRevision(ctx, id, actor.ClientID, strings.TrimSpace(operationID))
	if err != nil {
		return nil, err
	}
	if reserved > 0 && reserved != expected {
		return nil, ErrOperationConflict
	}
	d := current
	if reserved > 0 || current.Revision != expected {
		d, err = s.pinnedDocument(ctx, current, expected)
		if err != nil {
			return nil, err
		}
		// A reservation behind the saved base has nothing left to publish.
		if d.BaseSHA256 != current.BaseSHA256 || d.Absent != current.Absent {
			err := ErrRevisionConflict
			if reserved > 0 {
				if settleErr := s.settleRejectedReservation(ctx, d, operationID, actor, err); settleErr != nil {
					return nil, settleErr
				}
			}
			return nil, err
		}
	}
	result, err := s.saveLocked(ctx, p, d, operationID, actor)
	if err != nil && reserved > 0 {
		if settleErr := s.settleRejectedReservation(ctx, d, operationID, actor, err); settleErr != nil {
			return nil, settleErr
		}
	}
	return result, err
}

type saveReplay struct {
	document *Document
	mutation *Mutation
}

// findSaveReplay validates reused input; nil means the operation ID is unused.
func (s *Service) findSaveReplay(ctx context.Context, p *project.Project, id, operationID string, actor saveActor, expected int64) (*saveReplay, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, fmt.Errorf("operation id required")
	}
	inputDigest := saveInputDigest(strings.TrimSpace(id), strings.TrimSpace(p.ID), actor, expected)
	existing, getErr := s.store.Mutation(ctx, operationID)
	if errors.Is(getErr, ErrNotFound) {
		return nil, nil
	}
	if getErr != nil {
		return nil, getErr
	}
	if existing.InputDigest != inputDigest {
		return nil, ErrOperationConflict
	}
	d, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	return &saveReplay{document: d, mutation: existing}, nil
}

func (s *Service) resumeSave(ctx context.Context, p *project.Project, replay *saveReplay) (*Document, error) {
	if err := checkWorkspace(p, replay.document); err != nil {
		return nil, err
	}
	if replay.mutation.Status == "complete" {
		return s.mutationResponse(ctx, replay.document, replay.mutation)
	}
	release, err := s.reserveDocumentSource(ctx, p, replay.document)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.finishMutation(ctx, p, replay.document, replay.mutation)
}

// saveLocked journals and applies one save of the document's current draft.
// The caller holds the document lock and has settled who is saving.
func (s *Service) saveLocked(ctx context.Context, p *project.Project, d *Document, operationID string, actor saveActor) (*Document, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, err
	}
	release, err := s.reserveDocumentSource(ctx, p, d)
	if err != nil {
		return nil, err
	}
	defer release()
	if len(d.CRDTUpdate) == 0 {
		if err := s.replicaProjection(ctx, d, nil); err != nil {
			return nil, err
		}
	}
	m, err := s.prepareSaveMutation(ctx, d, operationID, actor)
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertMutation(ctx, m); err != nil {
		return nil, err
	}
	return s.finishMutation(ctx, p, d, m)
}

// prepareSaveMutation journals the exact bytes one save will publish. An
// absent document expects no file, so its mutation carries an empty hash.
func (s *Service) prepareSaveMutation(ctx context.Context, d *Document, operationID string, actor saveActor) (*Mutation, error) {
	checkpoint, err := s.publicationCheckpoint(ctx, d)
	if err != nil {
		return nil, err
	}
	head, err := s.store.replicaHead(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	content := serializeEOL(d.Draft, d.EOL)
	after, err := textfile.EncodeBounded(content, d.Encoding, textfile.LimitsForRaw(project.SourceWriteMaxBytes))
	if err != nil {
		return nil, err
	}
	root, err := s.liveRootPath(ctx, d.ProjectID, d.BranchID, d.RootID)
	if err != nil {
		return nil, err
	}
	var before []byte
	expected := d.BaseSHA256
	if d.Absent {
		expected = ""
	} else if before, err = readPublicationPreimage(root, d.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC()
	m := &Mutation{ID: strings.TrimSpace(operationID), BranchID: d.BranchID, ClientID: actor.ClientID,
		InputDigest: saveInputDigest(d.ID, d.ProjectID, actor, d.Revision),
		DocumentID:  d.ID, ProjectID: d.ProjectID, FileID: d.FileID,
		RootID: d.RootID, Path: d.Path, ExpectedSHA256: expected,
		AfterSHA256: textfile.SHA256(after), Encoding: d.Encoding, Content: content,
		DraftRevision: d.Revision, EOL: d.EOL, Checkpoint: checkpoint, BeforeCheckpoint: head.PublishedCheckpoint,
		BeforeBytes: before, AfterBytes: after, Status: "prepared",
		SessionID: actor.SessionID, Turn: actor.Turn, CreatedAt: now, UpdatedAt: now,
		Origin: actor.Origin, PersonID: actor.PersonID, ToolCallID: actor.ToolCallID, ToolName: actor.ToolName}
	return m, nil
}

func (s *Service) finishMutation(ctx context.Context, p *project.Project, d *Document, m *Mutation) (*Document, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, err
	}
	if m.BranchID != d.BranchID {
		return nil, ErrNotFound
	}
	current, err := s.store.Get(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	d = current
	if m.Status == "prepared" {
		result, err := publishMutation(p, m)
		if err != nil {
			status := "failed"
			if errors.Is(err, project.ErrSourceWriteConflict) {
				status = "conflict"
			}
			if settleErr := s.settlePublicationFailure(ctx, d, m, status, err); settleErr != nil {
				return nil, errors.Join(err, settleErr)
			}
			return nil, err
		}
		m.AfterSHA256, m.BeforeBytes, m.AfterBytes = result.SHA256, result.Before, result.After
		m.Status, m.UpdatedAt = "file_applied", time.Now().UTC()
		if err := s.store.UpdateMutation(ctx, m); err != nil {
			return nil, err
		}
	}
	if m.Status != "file_applied" && m.Status != "complete" {
		return nil, ErrRevisionConflict
	}
	if m.Status == "file_applied" {
		if err := s.commit(ctx, p, d, m); err != nil {
			return nil, err
		}
	}
	return s.mutationResponse(ctx, d, m)
}

// publishMutation lands the journaled bytes: an exclusive create when the
// document expected no file, a guarded replacement otherwise.
func publishMutation(p *project.Project, m *Mutation) (*project.SourceWriteResult, error) {
	req := project.SourceWriteRequest{Path: m.Path, RootID: m.RootID, Content: m.Content, Encoding: m.Encoding, BaseSHA256: m.ExpectedSHA256}
	if m.Creates() {
		return project.ApplySourceWriteCreate(p, req)
	}
	return project.ApplySourceWriteCAS(p, req)
}

// Creates reports a publication that expects no file at its path.
func (m *Mutation) Creates() bool { return m.ExpectedSHA256 == "" }

// commit atomically records attribution, events, document state, and completion.
func (s *Service) commit(ctx context.Context, p *project.Project, d *Document, m *Mutation) error {
	if err := s.replicaProjection(ctx, d, nil); err != nil {
		return err
	}
	next := *d
	next.replicaCommit = nil
	next.PublishedRevision = m.DraftRevision
	next.publishedCheckpoint = m.Checkpoint
	next.BaseContent, next.BaseSHA256 = strings.ReplaceAll(m.Content, "\r\n", "\n"), m.AfterSHA256
	next.SizeBytes = int64(len(m.AfterBytes))
	next.BaseEOL, next.BaseMixedEOL = m.EOL, false
	next.Diverged = !s.mutationMatchesDisk(ctx, m)
	next.Absent = false
	if d.Revision == m.DraftRevision {
		next.MixedEOL = false
	}
	next.Dirty = next.Draft != next.BaseContent || next.EOL != next.BaseEOL || next.MixedEOL != next.BaseMixedEOL
	// The saved draft resolves the pending agent edit.
	next.HeldAgentVersionID = ""
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	done := *m
	done.Checkpoint = nil
	done.BeforeCheckpoint = nil
	done.Status, done.Error, done.Content, done.BeforeBytes, done.AfterBytes = "complete", "", "", nil, nil
	done.UpdatedAt = time.Now().UTC()
	op := api.SourceChangeOpWrite
	if m.Creates() {
		op = api.SourceChangeOpCreate
	}
	change := sourcefeed.Change{
		ProjectID: d.ProjectID, WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID: d.RootID, Path: d.Path, Op: op, Origin: m.Origin,
		SessionID: m.SessionID, Turn: m.Turn, ToolCallID: m.ToolCallID, AfterSHA256: m.AfterSHA256,
		AbsPath: filepath.Join(rootPath(p, d.RootID), filepath.FromSlash(d.Path)),
	}
	var delivery *sourcefeed.StagedDelivery
	textAfter, err := s.publicationTextState(ctx, d, m.Checkpoint)
	if err != nil {
		return err
	}
	if textAfter != nil {
		textAfter.Revision = m.DraftRevision
	}
	record := sourceledger.RecordInput{
		TextAfter: textAfter,
		ProjectID: m.ProjectID, BranchID: d.BranchID,
		RootID: m.RootID, Path: m.Path, FileID: d.FileID, EntryKind: sourceledger.EntryKindFile,
		Op: op, Origin: m.Origin, PersonID: m.PersonID,
		SessionID: m.SessionID, Turn: m.Turn, OperationID: m.ID,
		ToolCallID: m.ToolCallID, ToolName: m.ToolName,
		AfterSHA256: m.AfterSHA256, After: m.AfterBytes, AfterSize: int64(len(m.AfterBytes)),
	}
	if m.Creates() {
		// A recreated path is a new file to the ledger.
		record.FileID = ""
	} else {
		textBefore, err := s.publicationTextState(ctx, d, m.BeforeCheckpoint)
		if err != nil {
			return err
		}
		if textBefore != nil {
			textBefore.Revision = d.PublishedRevision
		}
		record.TextBefore = textBefore
		record.Before, record.BeforeSize = m.BeforeBytes, int64(len(m.BeforeBytes))
	}
	prepared, err := s.ledger.Prepare(ctx, []sourceledger.RecordInput{record})
	if err != nil {
		return err
	}
	defer prepared.Close()
	err = s.store.Tx(ctx, func(tx *sql.Tx) error {
		tracked, err := prepared.CommitTx(ctx, tx)
		if err != nil {
			return err
		}
		if tracked.FileID != "" {
			next.FileID = tracked.FileID
		}
		delivery, err = sourcefeed.EmitTx(ctx, tx, change)
		if err != nil {
			return err
		}
		if err := s.store.UpdateCASTx(ctx, tx, &next, d.Revision); err != nil {
			return err
		}
		response := s.withParticipants(&next)
		responseJSON, err := json.Marshal(response)
		if err != nil {
			return err
		}
		done.ResponseJSON = string(responseJSON)
		return s.store.UpdateMutationTx(ctx, tx, &done)
	})
	if err != nil {
		return err
	}
	*m = done
	*d = next
	delivery.DeliverCommitted()
	s.changed(ctx, d, false)
	return nil
}

// Compact receipts prevent duplicate publication on reconnect.
func (s *Service) mutationResponse(ctx context.Context, current *Document, m *Mutation) (*Document, error) {
	if m.ReplayCompacted {
		if err := s.replicaProjection(ctx, current, nil); err != nil {
			return nil, err
		}
		return s.withParticipants(current), nil
	}
	return decodeMutationResponse(m)
}

func decodeMutationResponse(m *Mutation) (*Document, error) {
	if strings.TrimSpace(m.ResponseJSON) == "" {
		return nil, fmt.Errorf("editor save %s has no committed response", m.ID)
	}
	var response Document
	if err := json.Unmarshal([]byte(m.ResponseJSON), &response); err != nil {
		return nil, fmt.Errorf("decode editor save %s response: %w", m.ID, err)
	}
	return &response, nil
}

// A remembered byte order applies only while the bytes need explicit decoding.
func readDocumentSource(p *project.Project, d *Document) (*project.SourceReadResult, error) {
	_, read, err := observeDocumentSource(p, d)
	return read, err
}

// observeDocumentSource reads the document's file and keeps the observation
// for the ledger. An undecodable file answers its observation beside the error.
func observeDocumentSource(p *project.Project, d *Document) (*project.SourceReadObservation, *project.SourceReadResult, error) {
	if err := checkWorkspace(p, d); err != nil {
		return nil, nil, err
	}
	req := project.SourceReadRequest{Path: d.Path, RootID: d.RootID}
	observation, err := project.ObserveProjectSource(p, req)
	if err != nil {
		return nil, nil, err
	}
	read, err := observation.Project()
	decodeAs := documentDecodeAs(d)
	if decodeAs == "" {
		return observation, read, err
	}
	var unsupported *project.SourceUnsupportedEncodingError
	if err != nil && !errors.As(err, &unsupported) {
		return observation, nil, err
	}
	if err == nil && (!read.Binary || read.OverLimit || strings.HasPrefix(read.MIME, "image/")) {
		return observation, read, nil
	}
	req.DecodeAs = decodeAs
	observation, err = project.ObserveProjectSource(p, req)
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

func validateDocumentAdmission(read *project.SourceReadResult) error {
	if err := validateEditableSource(read); err != nil {
		return err
	}
	if !read.Writable {
		return ErrReadOnly
	}
	return nil
}

func validateEditableSource(read *project.SourceReadResult) error {
	if read.OverLimit {
		return project.ErrSourceWriteTooLarge
	}
	if read.Binary || read.Encoding == "" {
		return project.ErrSourceBinary
	}
	return nil
}

func (s *Service) checked(ctx context.Context, id, projectID string) (*Document, error) {
	d, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if d.ProjectID != strings.TrimSpace(projectID) {
		return nil, ErrNotFound
	}
	return d, nil
}

func (s *Service) checkedRevision(ctx context.Context, id, projectID, clientID string, expected int64) (*Document, error) {
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	if d.Revision != expected {
		return nil, ErrRevisionConflict
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, ErrReplicaIdentity
	}
	return d, nil
}

func (s *Service) changed(ctx context.Context, d *Document, contentChanged bool) {
	if s.onChange != nil {
		s.projectWorkspacePresentation(ctx, d)
		s.onChange(ctx, Change{Document: s.withParticipants(d), ContentChanged: contentChanged})
	}
}

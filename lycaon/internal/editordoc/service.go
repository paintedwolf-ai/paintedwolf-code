package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

type Ledger interface {
	sourceledger.FileTracker
	RecordTx(context.Context, *sql.Tx, sourceledger.RecordInput) error
	// RecordFileTx records a publication and answers the file identity it
	// landed on, which a recreated path receives fresh.
	RecordFileTx(context.Context, *sql.Tx, sourceledger.RecordInput) (sourceledger.TrackedFile, error)
	RecordHeldEditTx(context.Context, *sql.Tx, sourceledger.HeldEdit) (string, error)
}

type Service struct {
	agentReads      agentReadCache
	replicas        replicaRuntime
	store           *Store
	ledger          Ledger
	history         *sourceledger.History
	sourceMutations *projectsource.SourceMutationService

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

func (s *Service) SetSourceMutations(mutations *projectsource.SourceMutationService) {
	s.sourceMutations = mutations
}

func (s *Service) reserveDocumentSource(ctx context.Context, p *project.Project, d *Document) (func(), error) {
	var pending bool
	err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM editor_retargets r JOIN source_mutations m ON m.id=r.source_operation_id WHERE r.project_id=? AND r.branch_id=? AND r.root_id=? AND (?=r.from_path OR substr(?,1,length(r.from_path)+1)=r.from_path||'/') AND (m.status IN ('prepared','file_applied','committed') OR COALESCE(json_extract(m.plan_json,'$.hold_started'),0)=1))`, d.ProjectID, d.BranchID, d.RootID, d.Path, d.Path).Scan(&pending)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, projectsource.ErrSourceBusy
	}
	return s.sourceMutations.Paths.ReserveSourcePath(rootPath(p, d.RootID), d.Path)
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

func New(store *Store, ledger Ledger, history *sourceledger.History, roots RootSource) *Service {
	if ledger == nil {
		panic("editor document ledger is required")
	}
	return &Service{store: store, ledger: ledger, history: history, roots: roots,
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
		observation, err := projectsource.ObserveProjectSource(p, projectsource.SourceReadRequest{Path: path, RootID: rootID, DecodeAs: decodeAs})
		if errors.Is(err, projectsource.ErrSourceNotFound) {
			d, absentErr := s.openAbsentDocument(ctx, p, path, rootID, clientID, options)
			// The file came back between the two looks; read it instead.
			if errors.Is(absentErr, projectsource.ErrSourceExists) && attempt == 0 {
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

// OpenedSource pairs the saved observation with the authoritative live draft.
type OpenedSource struct {
	Source   *projectsource.SourceReadResult
	Document *Document
}

// Retained identifies a replica the client already holds for the document it expects to open.
type Retained struct {
	DocumentID string
	Epoch      int64
	Vector     []byte
}

// OpenObserved projects an observed source and opens its live document when the text is editable.
func (s *Service) OpenObserved(ctx context.Context, p *project.Project, observation *projectsource.SourceReadObservation, decodeAs, clientID string, retained *Retained) (*OpenedSource, error) {
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

func (s *Service) openObservedDocument(ctx context.Context, p *project.Project, observation *projectsource.SourceReadObservation, read *projectsource.SourceReadResult, decodeAs, clientID string, options documentOpenOptions) (*Document, error) {
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

// trackInput describes an observed file to the ledger the way an open does.
func trackInput(p *project.Project, observation *projectsource.SourceReadObservation) sourceledger.TrackInput {
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
func readDocumentSource(p *project.Project, d *Document) (*projectsource.SourceReadResult, error) {
	_, read, err := observeDocumentSource(p, d)
	return read, err
}

// observeDocumentSource reads the document's file and keeps the observation
// for the ledger. An undecodable file answers its observation beside the error.
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

package editordoc

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type Ledger interface {
	sourceledger.FileTracker
	Prepare(context.Context, []sourceledger.RecordInput) (sourceledger.PreparedRecording, error)
	PrepareHeldEdit(context.Context, sourceledger.HeldEdit) (sourceledger.PreparedHeldEdit, error)
}

type serviceCore struct {
	replicas    replicaRuntime
	store       *Store
	ledger      Ledger
	history     *sourceledger.History
	sourcePaths *projectsource.SourcePaths
	roots       RootSource
	ops         sync.RWMutex
	docLocks    keyedLocks
	onChange    func(context.Context, Change)
}

type Service struct {
	serviceCore
	PresenceService
	AgentService
	SnapshotService
	RewindService
	LifecycleService
}

type PresenceService struct {
	s *Service
	authorshipTracker
	presenceMu     sync.Mutex
	presenceClosed bool
	participants   map[string]map[string]Participant
	windowNumbers  map[string]int
	clientPeople   map[string]string
}

type authorshipTracker struct{ s *Service }

type AgentService struct {
	s          *Service
	agentReads agentReadCache
	agentBatchPlanner
	agentEditPlanner
}

type agentBatchPlanner struct{ s *Service }
type agentEditPlanner struct{ s *Service }

type SnapshotService struct {
	s *Service
	savePublisher
	saveCommitter
	documentOpener
}

type savePublisher struct{ s *Service }
type saveCommitter struct{ s *Service }
type documentOpener struct{ s *Service }

type RewindService struct {
	s *Service
	semanticRewinder
	replicaManager
	replicaCommitter
}

type semanticRewinder struct{ s *Service }
type replicaManager struct{ s *Service }
type replicaCommitter struct{ s *Service }

type LifecycleService struct {
	s *Service
	filesystemSynchronizer
	externalReconciler
}

type filesystemSynchronizer struct{ s *Service }
type externalReconciler struct{ s *Service }

func (l *LifecycleService) SetSourcePaths(paths *projectsource.SourcePaths) {
	l.s.sourcePaths = paths
}

// Change describes the projection needed after one document transition.
type Change struct {
	Document       *Document
	ContentChanged bool
}

// RootSource answers where a project's roots point right now.
type RootSource interface {
	Get(ctx context.Context, projectID string) (*project.Project, error)
}

func New(store *Store, ledger Ledger, history *sourceledger.History, roots RootSource) *Service {
	if ledger == nil {
		panic("editor document ledger is required")
	}
	s := &Service{
		serviceCore: serviceCore{store: store, ledger: ledger, history: history, roots: roots},
		PresenceService: PresenceService{
			windowNumbers: make(map[string]int),
			clientPeople:  make(map[string]string),
			participants:  make(map[string]map[string]Participant),
		},
	}
	s.PresenceService.s = s
	s.authorshipTracker.s = s
	s.AgentService.s = s
	s.agentBatchPlanner.s = s
	s.agentEditPlanner.s = s
	s.SnapshotService.s = s
	s.savePublisher.s = s
	s.saveCommitter.s = s
	s.documentOpener.s = s
	s.RewindService.s = s
	s.semanticRewinder.s = s
	s.replicaManager.s = s
	s.replicaCommitter.s = s
	s.LifecycleService.s = s
	s.filesystemSynchronizer.s = s
	s.externalReconciler.s = s
	return s
}

func (l *LifecycleService) SetOnChange(fn func(context.Context, Change)) { l.s.onChange = fn }

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
func (c *SnapshotService) Snapshot(ctx context.Context, projectID, documentID string, expectedRevision int64) (*Document, error) {
	if expectedRevision < 1 {
		return nil, ErrRevisionConflict
	}
	return c.s.Pin(ctx, projectID, documentID, expectedRevision)
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
func (o documentOpener) Open(ctx context.Context, p *project.Project, path, rootID, decodeAs, clientID string, retained *Retained) (*Document, error) {
	s := o.s
	options := documentOpenOptions{join: true, retained: retained}
	for attempt := 0; ; attempt++ {
		observation, err := projectsource.ObserveProjectSource(p, projectsource.SourceReadRequest{Path: path, RootID: rootID, DecodeAs: decodeAs})
		if errors.Is(err, projectsource.ErrSourceNotFound) {
			d, absentErr := s.openAbsentDocument(ctx, p, path, rootID, clientID, options)
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
func (o documentOpener) OpenObserved(ctx context.Context, p *project.Project, observation *projectsource.SourceReadObservation, decodeAs, clientID string, retained *Retained) (*OpenedSource, error) {
	read, err := observation.Project()
	if err != nil {
		return nil, err
	}
	opened := &OpenedSource{Source: read}
	if read.OverLimit || read.Binary || read.Encoding == "" || !read.Writable {
		return opened, nil
	}
	opened.Document, err = o.s.openObservedDocument(ctx, p, observation, read, decodeAs, clientID, documentOpenOptions{requireWritable: true, retained: retained})
	return opened, err
}

type documentOpenOptions struct {
	requireWritable bool
	join            bool
	retained        *Retained
}

func (o documentOpenOptions) projectionVector(d *Document) func(head *ReplicaHead) []byte {
	return func(head *ReplicaHead) []byte {
		if o.retained == nil || o.retained.DocumentID != d.ID || o.retained.Epoch != head.Epoch {
			return nil
		}
		return o.retained.Vector
	}
}

// ObserveDisk reconciles through the same transition as filesystem observation.
func (l *LifecycleService) ObserveDisk(ctx context.Context, p *project.Project, id, clientID string) (*Document, error) {
	s := l.s
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



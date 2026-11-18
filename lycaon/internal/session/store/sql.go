package store

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/visual"
)

// SessionRevisionSource orders all session-event publishers.
type SessionRevisionSource interface {
	NextSessionRevision() uint64
}

// PromptPendingSource decides from a session's unsettled human prompt ids
// whether any still waits to start.
type PromptPendingSource interface {
	PromptPendingAmong(sessionID string, unsettled []string) bool
}

type sqlSpillState struct {
	spillReconciled      scopedstore.LRU[struct{}]
	spillReconcileStates spillRepairRegistry
}

type sqlEvidenceCaches struct {
	// lostBodies holds digests already reported missing.
	lostBodies scopedstore.LRU[struct{}] // sha256 → reported

	untrustedMu sync.Mutex
	// Each entry keeps count and cache warmth together.
	untrustedRecords scopedstore.LRU[untrustedCount] // sessionID → cached count

	secretExposureMu sync.Mutex
	// Each entry keeps count and cache warmth together.
	secretExposureRecords scopedstore.LRU[secretExposureCount] // sessionID → cached count
}

type sqlCore struct {
	db               db.ReadHandle
	queries          *db.Queries
	outbox           *eventoutbox.Outbox
	sessionRevisions SessionRevisionSource
	promptPending    PromptPendingSource
	dataDir          string
	mutationLocks    [64]sync.Mutex
	peopleStore      *peoplestore.Store
	// artifacts binds minted evidence handles onto visual artifact rows.
	artifacts *visual.Records
	sqlSpillState
	sqlEvidenceCaches
}

type sqlSessions struct{ core *sqlCore }
type sqlTranscript struct{ core *sqlCore }
type sqlCompaction struct{ core *sqlCore }
type sqlTurns struct{ core *sqlCore }
type sqlSubmissions struct{ core *sqlCore }
type sqlAttachmentBlobs struct{ core *sqlCore }
type sqlEvidence struct{ core *sqlCore }
type sqlRewind struct{ core *sqlCore }

// Repositories shares database transactions and session mutation serialization.
type Repositories struct {
	core        *sqlCore
	People      *peoplestore.Store
	Sessions    SessionRepository
	Transcript  TranscriptRepository
	Compaction  CompactionRepository
	Turns       TurnRepository
	Submissions SubmissionRepository
	Attachments AttachmentRepository
	Evidence    EvidenceRepository
	Rewind      RewindRepository
}

func (s *sqlCore) lockMutation(id string) func() {
	var shard uint
	for i := 0; i < len(id); i++ {
		shard = shard*33 + uint(id[i])
	}
	lock := &s.mutationLocks[shard%uint(len(s.mutationLocks))]
	lock.Lock()
	return lock.Unlock
}

// SetEventOutbox makes transcript mutations publish from their transaction.
func (s *Repositories) SetEventOutbox(outbox *eventoutbox.Outbox) {
	s.core.outbox = outbox
}

// SetArtifactRecords lets evidence commits stamp handles onto artifact rows.
func (s *Repositories) SetArtifactRecords(records *visual.Records) {
	s.core.artifacts = records
}

// SetSessionRevisions sets the shared session-event revision source.
func (s *Repositories) SetSessionRevisions(src SessionRevisionSource) {
	s.core.sessionRevisions = src
}

// SetPromptPending sets the source of prompt_pending on outbox session events.
func (s *Repositories) SetPromptPending(src PromptPendingSource) {
	s.core.promptPending = src
}

// SetDataDir sets the device root for spill reclamation.
func (s *Repositories) SetDataDir(dir string) {
	if s != nil && s.core != nil {
		s.core.dataDir = strings.TrimSpace(dir)
	}
}

// MutationEventsOutboxed reports whether transcript mutations publish observer events.
func (s *sqlTranscript) MutationEventsOutboxed() bool {
	return s != nil && s.core != nil && s.core.outbox != nil
}

// NewSQL constructs repositories over one durable transaction domain.
func NewSQL(database db.ReadHandle) *Repositories {
	core := &sqlCore{
		db:          database,
		queries:     db.New(database),
		peopleStore: peoplestore.New(database),
	}
	return &Repositories{
		core: core, People: core.peopleStore,
		Sessions:    &sqlSessions{core: core},
		Transcript:  &sqlTranscript{core: core},
		Compaction:  &sqlCompaction{core: core},
		Turns:       &sqlTurns{core: core},
		Submissions: &sqlSubmissions{core: core},
		Attachments: &sqlAttachmentBlobs{core: core},
		Evidence:    &sqlEvidence{core: core},
		Rewind:      &sqlRewind{core: core},
	}
}

// DB exposes the database for shared read-side projections.
func (s *Repositories) DB() db.Handle {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.db
}

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
type sqlSessionViews struct{ core *sqlCore }
type sqlTranscript struct{ core *sqlCore }
type sqlCompaction struct{ core *sqlCore }
type sqlTurns struct{ core *sqlCore }
type sqlTurnTelemetry struct{ core *sqlCore }
type sqlSubmissions struct{ core *sqlCore }
type sqlAttachmentBlobs struct{ core *sqlCore }
type sqlEvidence struct{ core *sqlCore }
type sqlScreenGeneration struct{ core *sqlCore }
type sqlRewind struct{ core *sqlCore }

// SQL persists sessions and messages.
type SQL struct {
	*sqlCore
	sqlSessions
	sqlSessionViews
	sqlTranscript
	sqlCompaction
	sqlTurns
	sqlTurnTelemetry
	sqlSubmissions
	sqlAttachmentBlobs
	sqlEvidence
	sqlScreenGeneration
	sqlRewind
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
func (s *SQL) SetEventOutbox(outbox *eventoutbox.Outbox) {
	s.outbox = outbox
}

// SetArtifactRecords lets evidence commits stamp handles onto artifact rows.
func (s *SQL) SetArtifactRecords(records *visual.Records) {
	s.artifacts = records
}

// SetSessionRevisions sets the shared session-event revision source.
func (s *SQL) SetSessionRevisions(src SessionRevisionSource) {
	s.sessionRevisions = src
}

// SetPromptPending sets the source of prompt_pending on outbox session events.
func (s *SQL) SetPromptPending(src PromptPendingSource) {
	s.promptPending = src
}

// SetDataDir sets the device root for spill reclamation.
func (s *SQL) SetDataDir(dir string) {
	if s != nil && s.sqlCore != nil {
		s.dataDir = strings.TrimSpace(dir)
	}
}

// MutationEventsOutboxed reports whether the store publishes message events.
func (s *SQL) MutationEventsOutboxed() bool {
	return s != nil && s.sqlCore != nil && s.outbox != nil
}

// NewSQL creates a store backed by the given database.
func NewSQL(database db.ReadHandle) *SQL {
	core := &sqlCore{
		db:          database,
		queries:     db.New(database),
		peopleStore: peoplestore.New(database),
	}
	return &SQL{
		sqlCore:             core,
		sqlSessions:         sqlSessions{core: core},
		sqlSessionViews:     sqlSessionViews{core: core},
		sqlTranscript:       sqlTranscript{core: core},
		sqlCompaction:       sqlCompaction{core: core},
		sqlTurns:            sqlTurns{core: core},
		sqlTurnTelemetry:    sqlTurnTelemetry{core: core},
		sqlSubmissions:      sqlSubmissions{core: core},
		sqlAttachmentBlobs:  sqlAttachmentBlobs{core: core},
		sqlEvidence:         sqlEvidence{core: core},
		sqlScreenGeneration: sqlScreenGeneration{core: core},
		sqlRewind:           sqlRewind{core: core},
	}
}

// DB exposes the store handle for read-side projections.
func (s *SQL) DB() db.Handle {
	if s == nil || s.sqlCore == nil {
		return nil
	}
	return s.db
}

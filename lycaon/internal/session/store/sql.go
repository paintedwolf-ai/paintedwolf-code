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

// SQL persists sessions and messages.
type SQL struct {
	db               db.ReadHandle
	queries          *db.Queries
	outbox           *eventoutbox.Outbox
	sessionRevisions SessionRevisionSource
	promptPending    PromptPendingSource
	dataDir          string

	spillReconciled scopedstore.LRU[struct{}]
	// lostBodies holds digests already reported missing.
	lostBodies           scopedstore.LRU[struct{}] // sha256 → reported
	spillReconcileStates spillRepairRegistry

	untrustedMu sync.Mutex
	// Each entry keeps count and cache warmth together.
	untrustedRecords scopedstore.LRU[untrustedCount] // sessionID → cached count

	secretExposureMu sync.Mutex
	// Each entry keeps count and cache warmth together.
	secretExposureRecords scopedstore.LRU[secretExposureCount] // sessionID → cached count
	mutationLocks         [64]sync.Mutex

	peopleStore *peoplestore.Store
	// artifacts binds minted evidence handles onto visual artifact rows.
	artifacts *visual.Records
}

func (s *SQL) lockMutation(id string) func() {
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
	if s != nil {
		s.dataDir = strings.TrimSpace(dir)
	}
}

// MutationEventsOutboxed reports whether the store publishes message events.
func (s *SQL) MutationEventsOutboxed() bool {
	return s != nil && s.outbox != nil
}

// NewSQL creates a store backed by the given database.
func NewSQL(database db.ReadHandle) *SQL {
	return &SQL{
		db:          database,
		queries:     db.New(database),
		peopleStore: peoplestore.New(database),
	}
}

// DB exposes the store handle for read-side projections.
func (s *SQL) DB() db.Handle {
	if s == nil {
		return nil
	}
	return s.db
}

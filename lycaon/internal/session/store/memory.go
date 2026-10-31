package store

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

type memoryTranscriptState struct {
	messages         map[string][]api.Message
	transcriptSeq    map[string]int64
	transcriptOrd    map[string]int64
	userTurns        map[string]map[string]int
	userTurnSequence map[string]int
	turnSourceBriefs map[string]map[string]string
	screenGeneration map[string]map[string]uint64
}

type memoryCompactionState struct {
	compactionAttempts map[string]messageview.CompactionAttempt
	compactionViews    map[string]CompactionView
	chunkProjections   map[string]map[string]messageview.ChunkProjection
	draftVersions      map[string][]api.DraftVersion
	worktreeBindings   map[string]WorktreeBinding
	worktreeIdentities map[string]string
}

type memoryEvidenceState struct {
	evidence              map[string]map[string]memoryEvidenceRow
	artifactHandles       visual.HandleBinder
	untrustedRecords      map[string]int
	secretExposureRecords map[string]int
}

type memorySubmissionState struct {
	promptSubmissions     map[string]PromptSubmission
	promptSubmissionSeq   map[string]int64
	promptAttachmentBlobs map[string]map[string]memoryPromptAttachmentBlob
}

type memoryRewindState struct {
	checkpoints      map[string]map[string]memoryCheckpoint
	rewindOperations map[string]RewindOperation
	rewindCreatedAt  map[string]time.Time
}

type memoryTurnState struct {
	modelLimits           map[string]ModelLimit
	turnCloseouts         map[string]string
	turns                 map[string]Turn
	turnHeads             map[string]string
	turnAttempts          map[string][]TurnAttempt
	liveModelOutputs      map[string]LiveModelOutput
	modelOutputs          map[string]ModelOutput
	turnSubmissions       map[string]string
	workerTurns           map[string][]string
	projectedModelOutputs map[string]int64
}

type memoryTelemetryState struct {
	turnClocks  map[string]map[string]TurnClock
	turnLoads   map[string][]TurnLoadReceipt
	turnLoadSeq int64
}

type memoryCore struct {
	owner           people.Person
	mu              sync.RWMutex
	sessions        map[string]*api.Session
	statusChangedAt map[string]time.Time
	memoryTranscriptState
	memoryCompactionState
	memoryEvidenceState
	memorySubmissionState
	memoryRewindState
	memoryTurnState
	memoryTelemetryState
}

type memorySessions struct{ core *memoryCore }
type memorySessionViews struct{ core *memoryCore }
type memoryTranscript struct{ core *memoryCore }
type memoryCompaction struct{ core *memoryCore }
type memoryTurns struct{ core *memoryCore }
type memoryTurnTelemetry struct{ core *memoryCore }
type memorySubmissions struct{ core *memoryCore }
type memoryAttachmentBlobs struct{ core *memoryCore }
type memoryEvidence struct{ core *memoryCore }
type memoryScreenGeneration struct{ core *memoryCore }
type memoryRewind struct{ core *memoryCore }

// Memory is a thread-safe in-memory session store.
type Memory struct {
	*memoryCore
	memorySessions
	memorySessionViews
	memoryTranscript
	memoryCompaction
	memoryTurns
	memoryTurnTelemetry
	memorySubmissions
	memoryAttachmentBlobs
	memoryEvidence
	memoryScreenGeneration
	memoryRewind
}

type memoryEvidenceRow struct {
	rec          evidence.Record
	supersededBy string
}

type memoryPromptAttachmentBlob struct {
	byteSize  int64
	createdAt time.Time
}

// NewMemory creates an empty in-memory session store.
func NewMemory() *Memory {
	core := &memoryCore{
		owner:           people.Person{ID: uuid.NewString(), Role: api.PersonRoleOwner},
		sessions:        make(map[string]*api.Session),
		statusChangedAt: make(map[string]time.Time),
		memoryTranscriptState: memoryTranscriptState{
			messages:         make(map[string][]api.Message),
			transcriptSeq:    make(map[string]int64),
			transcriptOrd:    make(map[string]int64),
			userTurns:        make(map[string]map[string]int),
			userTurnSequence: make(map[string]int),
			turnSourceBriefs: make(map[string]map[string]string),
			screenGeneration: make(map[string]map[string]uint64),
		},
		memoryCompactionState: memoryCompactionState{
			compactionViews:    make(map[string]CompactionView),
			draftVersions:      make(map[string][]api.DraftVersion),
			worktreeBindings:   make(map[string]WorktreeBinding),
			worktreeIdentities: make(map[string]string),
		},
		memoryEvidenceState: memoryEvidenceState{
			evidence:              make(map[string]map[string]memoryEvidenceRow),
			untrustedRecords:      make(map[string]int),
			secretExposureRecords: make(map[string]int),
		},
		memorySubmissionState: memorySubmissionState{
			promptSubmissions:     make(map[string]PromptSubmission),
			promptSubmissionSeq:   make(map[string]int64),
			promptAttachmentBlobs: make(map[string]map[string]memoryPromptAttachmentBlob),
		},
		memoryRewindState: memoryRewindState{
			rewindOperations: make(map[string]RewindOperation),
			rewindCreatedAt:  make(map[string]time.Time),
		},
		memoryTurnState: memoryTurnState{
			turns:                 make(map[string]Turn),
			turnHeads:             make(map[string]string),
			turnAttempts:          make(map[string][]TurnAttempt),
			liveModelOutputs:      make(map[string]LiveModelOutput),
			modelOutputs:          make(map[string]ModelOutput),
			turnSubmissions:       make(map[string]string),
			workerTurns:           make(map[string][]string),
			projectedModelOutputs: make(map[string]int64),
		},
		memoryTelemetryState: memoryTelemetryState{
			turnClocks: make(map[string]map[string]TurnClock),
			turnLoads:  make(map[string][]TurnLoadReceipt),
		},
	}
	return &Memory{
		memoryCore:             core,
		memorySessions:         memorySessions{core: core},
		memorySessionViews:     memorySessionViews{core: core},
		memoryTranscript:       memoryTranscript{core: core},
		memoryCompaction:       memoryCompaction{core: core},
		memoryTurns:            memoryTurns{core: core},
		memoryTurnTelemetry:    memoryTurnTelemetry{core: core},
		memorySubmissions:      memorySubmissions{core: core},
		memoryAttachmentBlobs:  memoryAttachmentBlobs{core: core},
		memoryEvidence:         memoryEvidence{core: core},
		memoryScreenGeneration: memoryScreenGeneration{core: core},
		memoryRewind:           memoryRewind{core: core},
	}
}

// MutationEventsOutboxed reports false: the memory store publishes no events.
func (*Memory) MutationEventsOutboxed() bool { return false }

// ReassignSessionsWorkspaceRoot retargets sessions from one root.
func (s *Memory) ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	projectID = strings.TrimSpace(projectID)
	fromRootID = strings.TrimSpace(fromRootID)
	if projectID == "" || fromRootID == "" {
		return nil
	}
	toRootID = strings.TrimSpace(toRootID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, sess := range s.sessions {
		if sess == nil || sess.ProjectID != projectID || sess.WorkspaceRootID != fromRootID {
			continue
		}
		sess.WorkspaceRootID = toRootID
		sess.WorkspacePath = ""
		sess.UpdatedAt = now
	}
	return nil
}

// HasActiveProjectSessions reports whether a project has unarchived active sessions in memory.
func (s *Memory) HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s == nil || s.memoryCore == nil {
		return false, nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.ProjectID != projectID || sess.ArchivedAt != nil {
			continue
		}
		if sess.Status == api.SessionStatusBusy || sess.ActivityAt.After(activeSince) {
			return true, nil
		}
	}
	for _, turn := range s.turns {
		if turn.ProjectID == projectID && turn.Status == TurnStatusRunning {
			return true, nil
		}
	}
	return false, nil
}

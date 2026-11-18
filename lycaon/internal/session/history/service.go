package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/limits"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
	GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
	ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error)
	ReadSourceTurn(ctx context.Context, id string, number int) (messageview.SourceTurn, error)
}

type Compaction interface {
	CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error)
	GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error)
	GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error)
	GetCompactionView(ctx context.Context, sessionID string) (*store.CompactionView, bool, error)
	PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error
	PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error
	PutCompactionView(ctx context.Context, sessionID string, view store.CompactionView) error
}

// Service owns bounded history projection and durable compaction scheduling.
type Service struct {
	sessionStore    Sessions
	transcriptStore Transcript
	compactionStore Compaction
	Gate            *lifecycle.State
	Workspace       *sessionscope.Service
	Limits          *limits.Service
	Compactor       compaction.ContextCompactor
	Runner          *Runner
	router          *llm.StaticModelRouter
	dataDir         string
	calibration     scopedstore.LRU[tokenCalibration]
	cutoffs         cutoffTracker
	recallAvailable func(context.Context, *api.Session) bool
}

func New(sessionStore Sessions, transcriptStore Transcript, compactionStore Compaction, gate *lifecycle.State, workspace *sessionscope.Service, limits *limits.Service, router *llm.StaticModelRouter, recall func(context.Context, *api.Session) bool) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, compactionStore: compactionStore, Gate: gate, Workspace: workspace, Limits: limits, router: router, Runner: NewRunner(), recallAvailable: recall}
}
func (m *Service) SetCompactor(compactor compaction.ContextCompactor) {
	m.Compactor = compactor
	m.Limits.SetFallback(compactor)
}
func (m *Service) SetDataDir(dir string) { m.dataDir = dir }

// ForgetSessionState releases calibration, cutoff, and attempt records after a reset or eviction.
func (m *Service) ForgetSessionState(sessionID string) {
	m.calibration.Delete(sessionID)
	m.cutoffs.sessions.Delete(sessionID)
	if m.Runner != nil {
		m.Runner.attempts.forget(sessionID)
	}
}

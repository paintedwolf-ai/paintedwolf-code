package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/limits"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	messageview.ChunkProjectionStore
	messageview.CompactionAttemptStore
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	GetMessagesAfterOrd(context.Context, string, int64, int) ([]api.Message, error)
	GetCompactionView(context.Context, string) (*store.CompactionView, bool, error)
	CompactionViewCurrent(context.Context, string, int64, string, int64) (bool, error)
	PutCompactionView(context.Context, string, store.CompactionView) error
}

// Service owns bounded history projection and durable compaction scheduling.
type Service struct {
	store           Store
	Gate            *lifecycle.State
	Workspace       *sessionscope.Service
	Limits          *limits.Service
	Compactor       compaction.ContextCompactor
	Runner          *Runner
	router          *llm.StaticModelRouter
	progress        progress.RunScopedStore
	dataDir         string
	calibration     scopedstore.LRU[compaction.PromptTokenCalibration]
	recallAvailable func(context.Context, *api.Session) bool
}

func New(store Store, gate *lifecycle.State, workspace *sessionscope.Service, limits *limits.Service, router *llm.StaticModelRouter, recall func(context.Context, *api.Session) bool) *Service {
	return &Service{store: store, Gate: gate, Workspace: workspace, Limits: limits, router: router, Runner: NewRunner(), recallAvailable: recall}
}
func (m *Service) SetCompactor(compactor compaction.ContextCompactor) {
	m.Compactor = compactor
	m.Limits.SetFallback(compactor)
}
func (m *Service) SetProgress(progress progress.RunScopedStore) { m.progress = progress }
func (m *Service) SetDataDir(dir string)                        { m.dataDir = dir }
func (m *Service) ForgetCalibration(sessionID string)           { m.calibration.Delete(sessionID) }

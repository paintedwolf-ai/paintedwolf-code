package batchcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
}
type State interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type Workflow interface {
	ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error
}
type Progress interface {
	Get(context.Context, string) string
}
type Verification interface {
	WorkflowGateState(context.Context, *api.Session, []api.Message) (bool, bool, bool, bool)
}
type Guidance interface {
	Emit(context.Context, string, anchor.ID, anchor.Envelope)
}
type Loop interface{ DisarmTimerBackstop(context.Context, string) }
type Service struct {
	sessionStore    Sessions
	transcriptStore Transcript
	state           State
	workflows       Workflow
	progress        Progress
	verification    Verification
	guidance        Guidance
	loop            Loop
	workerQueue     workeroutcomes.CycleLedger
	turns           scopedstore.LRU[bool]
}

func New(sessionStore Sessions, transcriptStore Transcript, state State, verification Verification, guidance Guidance) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, state: state, verification: verification, guidance: guidance}
}
func (m *Service) SetWorkflow(workflow Workflow)                 { m.workflows = workflow }
func (m *Service) SetProgress(progress Progress)                 { m.progress = progress }
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger) { m.workerQueue = workers }
func (m *Service) SetLoop(loop Loop)                             { m.loop = loop }

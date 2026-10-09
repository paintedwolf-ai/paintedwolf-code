package instructions

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	UserTurnOrdinal(context.Context, string) (int, error)
}
type Workflow interface {
	GetActive(context.Context, string) (*api.WorkflowRun, error)
	TryResolveUserFeedback(context.Context, string, string, string, string) error
	ApplyCoordinatorBatchEvent(context.Context, string, batch.Event, int) error
}
type IntentBoundary interface{ NoteUserIntentBoundary(string) }

// Service records user instructions and advances only their explicit boundaries.
type Service struct {
	store                Store
	transcript           *transcript.Service
	captures             *checkpointcontrol.Capture
	deliverKicks         func(context.Context, string) error
	progress             progress.RunScopedStore
	workflows            Workflow
	review               ReviewCheckpointer
	toolApprovalCoalesce IntentBoundary
	gateRepeatLedger     IntentBoundary
	writeRootRuntime     IntentBoundary
	listenRuntime        IntentBoundary
	loopbackRuntime      IntentBoundary
}

func New(store Store, transcript *transcript.Service, captures *checkpointcontrol.Capture, deliver func(context.Context, string) error) *Service {
	return &Service{store: store, transcript: transcript, captures: captures, deliverKicks: deliver}
}
func (m *Service) SetWorkflow(workflow Workflow)                   { m.workflows = workflow }
func (m *Service) SetProgress(progress progress.RunScopedStore)    { m.progress = progress }
func (m *Service) SetReviewCheckpointer(review ReviewCheckpointer) { m.review = review }
func (m *Service) SetIntentBoundaries(coalesce, repeat, write, listen, loopback IntentBoundary) {
	m.toolApprovalCoalesce = coalesce
	m.gateRepeatLedger = repeat
	m.writeRootRuntime = write
	m.listenRuntime = listen
	m.loopbackRuntime = loopback
}
func (m *Service) resetBatch(ctx context.Context, id string, message api.Message) {
	if m.workflows != nil && promptinput.VisibleIntent(message) {
		_ = m.workflows.ApplyCoordinatorBatchEvent(ctx, id, batch.EventVisibleUserMessage, 0)
	}
}

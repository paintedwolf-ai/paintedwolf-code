package instructions

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Transcript interface {
	ReadSourceTurn(ctx context.Context, id string, number int) (messageview.SourceTurn, error)
}
type WorkflowDomains struct {
	Batch    WorkflowBatch
	Feedback WorkflowFeedback
	Runs     WorkflowRuns
}
type WorkflowBatch interface {
	ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error
}
type WorkflowFeedback interface {
	TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error
}
type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}

type IntentBoundary interface{ NoteUserIntentBoundary(string) }

// Service records user instructions and advances only their explicit boundaries.
type Service struct {
	sessionStore     Sessions
	transcriptStore  Transcript
	transcript       *transcript.Service
	captures         *checkpointcontrol.Capture
	deliverKicks     func(context.Context, string) error
	waitEnds         func(string) []string
	progress         progress.RunScopedStore
	workflows        *WorkflowDomains
	review           ReviewCheckpointer
	gateRepeatLedger IntentBoundary
}

func New(sessionStore Sessions, transcriptStore Transcript, transcript *transcript.Service, captures *checkpointcontrol.Capture, deliver func(context.Context, string) error) *Service {
	return &Service{sessionStore: sessionStore, transcriptStore: transcriptStore, transcript: transcript, captures: captures, deliverKicks: deliver}
}
func (m *Service) SetWorkflow(workflow *WorkflowDomains)           { m.workflows = workflow }
func (m *Service) SetProgress(progress progress.RunScopedStore)    { m.progress = progress }
func (m *Service) SetReviewCheckpointer(review ReviewCheckpointer) { m.review = review }
func (m *Service) resetBatch(ctx context.Context, id string, message api.Message) {
	if m.workflows != nil && promptinput.VisibleIntent(message) {
		_ = m.workflows.Batch.ApplyCoordinatorBatchEvent(ctx, id, batch.EventVisibleUserMessage, 0)
	}
}

func (m *Service) SetRepeatBoundary(repeat IntentBoundary) { m.gateRepeatLedger = repeat }

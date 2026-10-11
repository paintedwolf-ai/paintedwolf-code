package projectcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	List(context.Context) ([]*api.Session, error)
	ReassignSessionsWorkspaceRoot(context.Context, string, string, string) error
	UpdateSession(context.Context, string, func(*api.Session)) error
	UserTurnOrdinal(context.Context, string) (int, error)
}
type Projects interface {
	Get(context.Context, string) (*project.Project, error)
}
type Workers interface {
	List(context.Context, string, ...api.WorkerStatus) ([]api.WorkerTask, error)
}
type WorkerAbort interface {
	AbortWorkersForRoot(context.Context, string, string, []projectroot.RootRef, string) error
}
type Stops interface {
	Abort(context.Context, string, string) error
}
type Status interface {
	PublishIdle(context.Context, string, api.SessionIdleDisposition)
}
type Admission interface {
	RoundComplete(context.Context, string) bool
	MaybePromote(context.Context, string)
}
type Batch interface{ Reconcile(context.Context, string) }
type Instructions interface {
	ActiveWorkflowRunID(context.Context, string) string
}
type Transcript interface {
	AppendPlain(context.Context, string, ...api.Message) error
}
type Anchors interface {
	Emit(context.Context, string, anchor.ID, anchor.Envelope)
}
type OverlayPromoter interface {
	PromoteOverlay(context.Context, string, string, api.PromoteOverlayInput) (api.WorkerMergeResult, error)
	RejectOverlay(context.Context, string, string, string) (api.OverlayRejectOutcome, error)
}
type Service struct {
	store           Sessions
	projects        Projects
	workers         Workers
	workerAbort     WorkerAbort
	stops           Stops
	status          Status
	admission       Admission
	batch           Batch
	instructions    Instructions
	transcript      Transcript
	anchors         Anchors
	overlayPromoter OverlayPromoter
}

func New(store Sessions, stops Stops, status Status, admission Admission, batch Batch, instructions Instructions, transcript Transcript) *Service {
	return &Service{store: store, stops: stops, status: status, admission: admission, batch: batch, instructions: instructions, transcript: transcript}
}
func (s *Service) SetProjects(projects Projects)      { s.projects = projects }
func (s *Service) SetWorkers(workers Workers)         { s.workers = workers }
func (s *Service) SetWorkerAbort(workers WorkerAbort) { s.workerAbort = workers }
func (s *Service) SetAnchors(anchors Anchors)         { s.anchors = anchors }

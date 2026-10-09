package closeoutevidence

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	LoadLedger(context.Context, string) (evidence.Ledger, error)
}
type Workers interface {
	ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error)
}
type Workflow interface {
	GetActive(context.Context, string) (*api.WorkflowRun, error)
	IsAmbientRun(*api.WorkflowRun) bool
}
type Service struct {
	store     Sessions
	workers   Workers
	workflows Workflow
}

func New(store Sessions) *Service                 { return &Service{store: store} }
func (m *Service) SetWorkers(workers Workers)     { m.workers = workers }
func (m *Service) SetWorkflow(workflows Workflow) { m.workflows = workflows }

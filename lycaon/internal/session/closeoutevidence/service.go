package closeoutevidence

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}

type Evidence interface {
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
}
type Workers interface {
	ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error)
}
type Workflow interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}
type Service struct {
	sessionStore  Sessions
	evidenceStore Evidence
	workers       Workers
	workflows     Workflow
}

func New(sessionStore Sessions, evidenceStore Evidence) *Service {
	return &Service{sessionStore: sessionStore, evidenceStore: evidenceStore}
}
func (m *Service) SetWorkers(workers Workers)     { m.workers = workers }
func (m *Service) SetWorkflow(workflows Workflow) { m.workflows = workflows }

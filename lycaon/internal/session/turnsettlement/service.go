package turnsettlement

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	SetSessionStatus(context.Context, string, api.SessionStatus) error
}
type Gate interface {
	InProgress(context.Context, string) bool
}
type Workflow interface {
	ReconcileTurnCompletion(context.Context, string) error
	MaybeDeliverTopologyReport(context.Context, string, string) error
	CurrentPhase(context.Context, string) string
}
type Workspace interface {
	ActivePath(context.Context, *api.Session) (string, error)
}
type Service struct {
	store                       Sessions
	gate                        Gate
	prompt                      *promptstate.MutexRegistry
	status                      *execution.Status
	workspace                   Workspace
	batch                       *batchcontrol.Service
	runtime                     *coordinator.Runtime
	workflows                   Workflow
	loopWorkflowSource          loopwake.LoopWorkflowSource
	workerQueue                 workeroutcomes.CycleLedger
	projectSandboxReconcile     ProjectSandboxReconcile
	deferredTurnSettlement      deferredTurnSettlementStore
	deferredWorkflowCompletions sync.Map
	engineStopping              atomic.Bool
}

func New(store Sessions, gate Gate, prompt *promptstate.MutexRegistry, status *execution.Status, workspace Workspace, batch *batchcontrol.Service) *Service {
	return &Service{store: store, gate: gate, prompt: prompt, status: status, workspace: workspace, batch: batch}
}
func (m *Service) SetRuntime(runtime *coordinator.Runtime) { m.runtime = runtime }
func (m *Service) SetWorkflow(workflow Workflow)           { m.workflows = workflow }
func (m *Service) SetWorkflowSource(source loopwake.LoopWorkflowSource) {
	m.loopWorkflowSource = source
}
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger) { m.workerQueue = workers }
func (m *Service) Defer(sessionID string, disposition api.SessionIdleDisposition) {
	m.deferredTurnSettlement.put(sessionID, disposition)
}
func (m *Service) Pending(sessionID string) bool {
	m.deferredTurnSettlement.mu.Lock()
	defer m.deferredTurnSettlement.mu.Unlock()
	_, ok := m.deferredTurnSettlement.bySession[sessionID]
	return ok
}
func (m *Service) PendingWorkflow(sessionID string) bool {
	_, ok := m.deferredWorkflowCompletions.Load(sessionID)
	return ok
}
func (m *Service) Forget(sessionID string) {
	m.deferredTurnSettlement.remove(sessionID)
	m.deferredWorkflowCompletions.Delete(sessionID)
}

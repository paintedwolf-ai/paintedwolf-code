package contractfixture

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type ErrScanCoordinator struct {
	GetErr         error
	FixtureSummary *wire.CodeScan
}

// withErrScanCoordinator serves scans from a coordinator that knows no rows.

func (e *ErrScanCoordinator) Summary(ctx context.Context, id string) (*wire.CodeScan, error) {
	if e.FixtureSummary != nil {
		return e.FixtureSummary, nil
	}
	return nil, e.GetErr
}

func (e *ErrScanCoordinator) PublishSourceGeneration(context.Context, string) (sourcesnapshot.Snapshot, string, error) {
	return sourcesnapshot.Snapshot{}, "", e.GetErr
}

func (e *ErrScanCoordinator) Enqueue(ctx context.Context, req scan.EnqueueRequest) (*wire.CodeScan, error) {
	return &wire.CodeScan{ID: "x"}, nil
}

func (e *ErrScanCoordinator) Get(ctx context.Context, id string) (*wire.CodeScan, error) {
	return nil, e.GetErr
}

func (e *ErrScanCoordinator) List(ctx context.Context, canonicalPaths []string, limit int) ([]wire.CodeScan, error) {
	return nil, nil
}

func (e *ErrScanCoordinator) ListPage(ctx context.Context, canonicalPaths []string, query scan.PageQuery) (wire.CodeScanPage, error) {
	return wire.CodeScanPage{}, e.GetErr
}

func (e *ErrScanCoordinator) ListBySessionID(ctx context.Context, sessionID string) ([]wire.CodeScan, error) {
	return nil, nil
}

func (e *ErrScanCoordinator) ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]wire.CodeScan, error) {
	return nil, nil
}

func (e *ErrScanCoordinator) LatestForDelegation(ctx context.Context, delegationID string, categories []wire.ScanCategory) (*wire.CodeScan, error) {
	return nil, nil
}

func (e *ErrScanCoordinator) Query(ctx context.Context, req scan.QueryRequest) (*wire.ScanQueryResponse, error) {
	return nil, e.GetErr
}

func (e *ErrScanCoordinator) Compare(ctx context.Context, _, _ string) (*scan.Comparison, error) {
	return nil, e.GetErr
}

func (e *ErrScanCoordinator) PreviousComplete(ctx context.Context, _ wire.CodeScan) (*wire.CodeScan, error) {
	return nil, e.GetErr
}

func (e *ErrScanCoordinator) ResolveID(ctx context.Context, path, prefix string) (string, error) {
	return "", nil
}

func (c *ErrScanCoordinator) SnapshotStore() *sourcesnapshot.Store { return nil }

func NewInMemoryDelegationServer(t *testing.T) *api.Server {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	delegationStore := delegation.NewMemoryStore()
	workersCfg := worker.DefaultWorkersConfig()
	queue := worker.NewInMemoryQueue(workersCfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(workersCfg)
	delegationMgr := delegation.NewManager(delegationStore, queue, mgr, nil)
	reg := project.NewMemoryRegistry()
	delegationMgr.Projects = reg
	snap := &board.SnapshotBuilder{Delegations: delegationStore, Workers: queue, Repo: repotest.NewProvider(t)}
	return api.NewServer(RequiredTestDeps(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: reg, Sessions: mgr}, Workflow: api.WorkflowDependencies{
		Delegations: delegationMgr, Workers: queue, Board: snap}}), nil, api.TestAPIToken)
}

func WithErrScanCoordinator(d *api.Dependencies) {
	d.Scans.ScanCoordinator = &ErrScanCoordinator{GetErr: sql.ErrNoRows}
}

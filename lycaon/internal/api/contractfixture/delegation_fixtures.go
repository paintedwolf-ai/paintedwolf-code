package contractfixture

import (
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
)

func NewDelegationTestFixture(t *testing.T) (*hostapi.Server, project.Registry, func()) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewManager(store, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	delegationStore := delegation.NewMemoryStore()
	workersCfg := worker.DefaultWorkersConfig()
	queue := worker.NewInMemoryQueue(workersCfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(workersCfg)
	exec := worker.NewLocalWorkerExecutor(mgr, queue)
	exec.SetPromptInjects(promptstest.InjectRenderer(t))
	reg := project.NewMemoryRegistry()
	delegationMgr := delegation.NewManager(delegationStore, queue, mgr, nil)
	delegationMgr.Projects = reg
	boardSnap := &board.SnapshotBuilder{Delegations: delegationStore, Workers: queue, Repo: repotest.NewProvider(t)}

	srv := hostapi.NewServer(RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: reg, Sessions: mgr}, Workflow: hostapi.WorkflowDependencies{
		Delegations: delegationMgr, Workers: queue, Board: boardSnap}}), nil, hostapi.TestAPIToken)

	poller := worker.NewLocalWorkerPoller(queue, exec, workersCfg, &worker.SessionOutcomeBridge{Inner: delegationMgr})
	queue.SetRunningCancel(poller.Abort)

	return srv, reg, func() { go func() { _ = poller.Run(t.Context()) }() }
}

func NewDelegationTestServer(t *testing.T) (*hostapi.Server, project.Registry) {
	t.Helper()
	srv, reg, _ := NewDelegationTestFixture(t)
	return srv, reg
}

// newDelegationTestServerRunningWorkers additionally runs the poller, so dispatched
// jobs are claimed and executed. Leg status is live from the moment of dispatch:
// a test using this must wait for a terminal state rather than assert a
// transient one.

func NewDelegationTestServerRunningWorkers(t *testing.T) (*hostapi.Server, project.Registry) {
	t.Helper()
	srv, reg, start := NewDelegationTestFixture(t)
	start()
	return srv, reg
}

// newDelegationTestFixture returns the server plus a function that starts the poller.

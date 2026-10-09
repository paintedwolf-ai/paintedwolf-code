package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/progress"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireDelegationWorkers() error {
	return b.delegations.BuildWorkers(b.startup.ctx, delegations.Dependencies{
		Git:       b.git.mgr,
		Workflows: b.workflows,
		Sessions:  b.sessions,
		Storage:   b.storage,
		Execution: b.execution,
		Scanning:  b.scanning,
		Catalog:   b.catalog,
		Agents:    b.agents,
		Providers: b.providers,
		Coordinator: func() *coordinator.Runtime {
			return b.server.Coordinator
		},
		StartOrchestratedTopology: func(ctx context.Context, sessionID string, run *wire.WorkflowRun) {
			if b.server.Server != nil {
				b.server.Server.Admin.Workflow.Topology.StartOrchestratedTopologyForRun(ctx, sessionID, run)
			}
		},
		Progress: func() progress.RunScopedStore {
			if b.boards != nil {
				return b.boards.Progress
			}
			return nil
		},
	})
}

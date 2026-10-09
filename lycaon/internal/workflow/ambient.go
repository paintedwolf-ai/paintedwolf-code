package workflow

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Ambient struct {
	Runs     runstate.RunsRepository
	Sessions session.Store
	Resolver *catalog.Resolver
	Starts   *lifecycle.Admission
}

// StartAmbient attaches an ambient workflow without a start-approval gate.
func (m *Ambient) StartAmbient(ctx context.Context, sessionID, workflowID, version string) (*api.WorkflowRun, error) {
	return m.Starts.Start(hostctx.WithAmbientAttach(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      workflowID,
		WorkflowVersion: version,
	})
}

// EnsureSessionWorkflow binds an active workflow before a new user request.
// Worker sessions retain their parent's execution scope.
func (m *Ambient) EnsureSessionWorkflow(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run != nil {
		return run, err
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, runstate.ErrNoActiveRun
	}
	if sess.IsWorkerChild() {
		return nil, nil
	}
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return nil, err
	}
	run, err = m.StartAmbient(ctx, sessionID, ref.ID, ref.Version)
	if errors.Is(err, runstate.ErrActiveRunExists) {
		// Keep a concurrently started workflow.
		return m.Runs.ActiveBySession(ctx, sessionID)
	}
	return run, err
}

// ParallelTaskMaxWorkers returns parallel_task.max_workers for the active phase, or 0 when unset.
func (m *Ambient) ParallelTaskMaxWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxWorkers
}

// ParallelTaskMaxReadWorkers returns the active read-worker limit.
func (m *Ambient) ParallelTaskMaxReadWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxReadWorkers
}

// ParallelTaskMaxWriteWorkers returns the active write-worker limit.
func (m *Ambient) ParallelTaskMaxWriteWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxWriteWorkers
}

// PhaseTouchPaths returns touch.paths for the active delegate phase.
func (m *Ambient) PhaseTouchPaths(ctx context.Context, sessionID string) []string {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || len(phase.TouchPaths) == 0 {
		return nil
	}
	return append([]string(nil), phase.TouchPaths...)
}

func (m *Ambient) activePhaseDef(ctx context.Context, sessionID string) (workflowdef.PhaseDef, bool) {
	if m == nil || sessionID == "" {
		return workflowdef.PhaseDef{}, false
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return workflowdef.PhaseDef{}, false
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return workflowdef.PhaseDef{}, false
	}
	phase, ok := manifest.PhaseByID(active.CurrentPhase)
	return phase, ok
}

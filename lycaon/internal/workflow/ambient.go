package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostctx"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// StartAmbient attaches an ambient workflow without a start-approval gate.
func (m *RunManager) StartAmbient(ctx context.Context, sessionID, workflowID, version string) (*api.WorkflowRun, error) {
	return m.start(hostctx.WithAmbientAttach(ctx), sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      workflowID,
		WorkflowVersion: version,
	}, "")
}

// EnsureSessionWorkflow binds an active workflow before a new user request.
// Worker sessions retain their parent's execution scope.
func (m *RunManager) EnsureSessionWorkflow(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run != nil {
		return run, err
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrNoActiveRun
	}
	if sess.IsWorkerChild() {
		return nil, nil
	}
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return nil, err
	}
	run, err = m.StartAmbient(ctx, sessionID, ref.ID, ref.Version)
	if errors.Is(err, ErrActiveRunExists) {
		// Keep a concurrently started workflow.
		return m.Store.ActiveBySession(ctx, sessionID)
	}
	return run, err
}

// IsAmbientRun identifies an ambient root from its persisted attachment policy.
func (m *RunManager) IsAmbientRun(run *api.WorkflowRun) bool {
	if m == nil || run == nil || workflowdef.RunHasParent(run) {
		return false
	}
	return strings.TrimSpace(run.AttachPolicy) == string(workflowdef.AttachPolicySessionCreate)
}

// ParallelTaskMaxWorkers returns parallel_task.max_workers for the active phase, or 0 when unset.
func (m *RunManager) ParallelTaskMaxWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxWorkers
}

// ParallelTaskMaxReadWorkers returns the active read-worker limit.
func (m *RunManager) ParallelTaskMaxReadWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxReadWorkers
}

// ParallelTaskMaxWriteWorkers returns the active write-worker limit.
func (m *RunManager) ParallelTaskMaxWriteWorkers(ctx context.Context, sessionID string) int {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || phase.ParallelTask == nil {
		return 0
	}
	return phase.ParallelTask.MaxWriteWorkers
}

// PhaseTouchPaths returns touch.paths for the active delegate phase.
func (m *RunManager) PhaseTouchPaths(ctx context.Context, sessionID string) []string {
	phase, ok := m.activePhaseDef(ctx, sessionID)
	if !ok || len(phase.TouchPaths) == 0 {
		return nil
	}
	return append([]string(nil), phase.TouchPaths...)
}

func (m *RunManager) activePhaseDef(ctx context.Context, sessionID string) (workflowdef.PhaseDef, bool) {
	if m == nil || sessionID == "" {
		return workflowdef.PhaseDef{}, false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return workflowdef.PhaseDef{}, false
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return workflowdef.PhaseDef{}, false
	}
	phase, ok := manifest.PhaseByID(active.CurrentPhase)
	return phase, ok
}

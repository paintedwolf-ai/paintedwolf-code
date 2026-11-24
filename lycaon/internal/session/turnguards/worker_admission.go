package turnguards

import (
	"context"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
)

func (m *Service) WorkerAdmission() workeradmission.WorkerCycleGuardDeps {
	if m == nil {
		return workeradmission.WorkerCycleGuardDeps{}
	}
	return workeradmission.WorkerCycleGuardDeps{
		Workers: m.workerQueue,
		MaxWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWorkers(ctx, sessionID)
		},
		MaxReadWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxReadWorkers(ctx, sessionID)
		},
		MaxWriteWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWriteWorkers(ctx, sessionID)
		},
		PhaseGuardState: func(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState {
			if m.workflows == nil {
				return workflowfacts.WorkflowPhaseGuardState{}
			}
			return m.workflows.Policy.ActivePhaseGuardState(ctx, sessionID)
		},
		RepoKnownEmpty: func(ctx context.Context, workspacePath string) bool {
			return repoinfo.MeasuredEmpty(ctx, m.repoProvider, workspacePath)
		},
	}
}

func (m *Service) SetTaskIdentityResolver(resolver workeradmission.TaskIdentityResolver) {
	m.taskIdentities = resolver
}

func (m *Service) SetAskInstructionCheck(check func(context.Context, string, map[string]any) error) {
	m.checkAskInstruction = check
}

func observeAskWorkerWrites(sess *api.Session, tool string, identity workeradmission.TaskIdentity, instructionReject *toolrejection.ToolReject, gc *oar.GuardContext) {
	if gc == nil || sess == nil || sess.Mode != api.SessionModeAsk {
		return
	}
	var reject *toolrejection.ToolReject
	switch tool {
	case "task":
		reject = toolrejection.AsToolReject(workeradmission.RequireAskReadScope(tool, identity.MutationCapable, true, identity.Scope))
		if reject != nil && identity.AgentType != "" {
			reject.Data["agent_type"] = identity.AgentType
		}
	case "instruct_worker":
		reject = instructionReject
	}
	if reject == nil || reject.Code != "SESSION_ASK_MODE_ACTIVE" {
		return
	}
	gc.Workers.AskWorkerWrites = true
	if gc.ObservationData == nil {
		gc.ObservationData = map[string]any{}
	}
	for k, v := range reject.Data {
		gc.ObservationData[k] = v
	}
	if pm, ok := reject.Data["profile_mutation_capable"].(bool); ok {
		gc.Workers.ProfileMutationCapable = pm
	}
	if sm, ok := reject.Data["scope_mode"].(string); ok {
		gc.Workers.ScopeMode = sm
	}
}

func (m *Service) resolveTaskIdentity(ctx context.Context, sess *api.Session, args map[string]any) (workeradmission.TaskIdentity, error) {
	identity, err := m.taskIdentities.Resolve(ctx, sess.ID, args)
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		return identity, err
	}
	ctx = curationctx.WithSession(ctx, curationctx.Session{SessionID: sess.ID, ProjectID: sess.ProjectID, OwnerPersonID: sess.OwnerPersonID, Posture: string(sess.Posture), Agent: "coordinator"})
	plane := &toolfeedback.BlockPlane{Pipeline: m.ToolPolicy.Pipeline, Renderer: m.Feedback.Renderer()}
	if rendered := plane.RejectObservation(ctx, "task", "coordinator", args, reject); rendered != nil {
		return identity, rendered
	}
	return identity, toolrejection.RenderReject(reject, m.Rejects)
}

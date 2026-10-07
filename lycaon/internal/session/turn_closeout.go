package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// renderHostNudge resolves session anchor bindings for immediate loop delivery.
func (m *Manager) renderHostNudge(
	ctx context.Context,
	sess *api.Session,
	id anchor.ID,
	data map[string]any,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	surface := "coordinator"
	if sess.IsWorkerChild() {
		surface = "worker"
	}
	stem := anchor.InformRenderFor(ctx, id, anchor.MatchContext{Surface: surface, SessionID: sess.ID})
	if stem == "" {
		return promptloop.HostNudge{}
	}
	return promptloop.HostNudge{
		Content:  workercloseout.RenderWorkerKick(ctx, m.renderWorkerKick, stem, data),
		SignalID: string(id),
	}
}

func (m *Manager) turnCloseoutNudge(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	cause promptloop.TurnCloseoutCause,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	data := map[string]any{
		"reason_text": cause.Text(),
		"llm_timeout": cause.Reason == promptloop.TurnCloseoutLLMTimeout,
	}
	if sess.IsWorkerChild() {
		id := anchor.WorkerCloseout
		if cause.Reason == promptloop.TurnCloseoutCanceled {
			id = anchor.WorkerCancelCloseout
			data = nil
			if strings.TrimSpace(cause.CancelReason) != "" {
				data = map[string]any{"cancel_reason": cause.CancelReason}
			}
		}
		return m.renderHostNudge(ctx, sess, id, data)
	}
	surfaceID := ""
	if rt := m.ensureCoordinatorRuntime(); rt != nil {
		surfaceID = rt.PromptTurnSurfaceID(sess.ID)
	}
	id := anchor.TurnCloseout
	if surface.SurfaceDeliversReport(surfaceID) {
		id = anchor.CoordinatorCloseout
	}
	return m.renderHostNudge(ctx, sess, id, data)
}

// surveyStreakNudge reports read-only streaks in coordinator sessions. An
// inspect turn's reads are its work, so its streak is not reported.
func (m *Manager) surveyStreakNudge(
	ctx context.Context,
	sess *api.Session,
	batches int,
	tools []string,
) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() || m.turnLoads.TurnKind(sess.ID) == turnload.KindInspect {
		return promptloop.HostNudge{}
	}
	return m.renderHostNudge(ctx, sess, anchor.TurnSurveyStreak, map[string]any{
		"batches": batches,
		"tools":   append([]string(nil), tools...),
	})
}

// secretWithheldNudge renders a withheld-request notice.
func (m *Manager) secretWithheldNudge(
	ctx context.Context,
	sess *api.Session,
	guidanceText string,
) promptloop.HostNudge {
	return m.renderHostNudge(ctx, sess, anchor.OutboundSecretWithheld, map[string]any{
		"guidance": strings.TrimSpace(guidanceText),
	})
}

func (m *Manager) iterationRunwayNudge(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	remaining int,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	if sess.IsWorkerChild() {
		return m.workerRunwayNudge(ctx, sess, remaining)
	}
	return m.renderHostNudge(ctx, sess, anchor.TurnIterationsLow, map[string]any{
		"remaining": remaining,
	})
}

// workerRunwayNudge tells any worker its ceiling is near and whether it may ask for more.
func (m *Manager) workerRunwayNudge(ctx context.Context, sess *api.Session, remaining int) promptloop.HostNudge {
	if m.workerQueue == nil {
		return promptloop.HostNudge{}
	}
	task, ok := m.workerQueue.Get(workercontext.Job(ctx))
	if !ok || task == nil {
		return promptloop.HostNudge{}
	}
	budget := m.workerToolBudgetForTask(ctx, task)
	return m.renderHostNudge(ctx, sess, anchor.WorkerIterationsLow, map[string]any{
		"remaining":      remaining,
		"max_tool_loops": task.MaxToolLoops,
		"at_host_max":    task.MaxToolLoops >= budget.Max,
		"request_open":   task.BudgetRequest != nil,
	})
}

// workerBudgetAnswerNudge renders how the coordinator answered a worker's
// budget: worker.budget.raised or worker.budget.declined.
func (m *Manager) workerBudgetAnswerNudge(id anchor.ID) func(context.Context, *api.Session, int, int) promptloop.HostNudge {
	return func(ctx context.Context, sess *api.Session, used, max int) promptloop.HostNudge {
		if m == nil || sess == nil || !sess.IsWorkerChild() {
			return promptloop.HostNudge{}
		}
		return m.renderHostNudge(ctx, sess, id, map[string]any{
			"tool_loops_used": used,
			"max_tool_loops":  max,
			"remaining":       max - used,
		})
	}
}

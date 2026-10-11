package turnnudges

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

// Render resolves session anchor bindings for immediate loop delivery.
func (m *Service) Render(
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
		Content:  workercloseout.RenderWorkerKick(ctx, m.renderKick, stem, data),
		SignalID: string(id),
	}
}

func (m *Service) Closeout(
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
		return m.Render(ctx, sess, id, data)
	}
	surfaceID := ""
	if rt := m.surface; rt != nil {
		surfaceID = rt.PromptTurnSurfaceID(sess.ID)
	}
	id := anchor.TurnCloseout
	if surface.SurfaceDeliversReport(surfaceID) {
		id = anchor.CoordinatorCloseout
	}
	return m.Render(ctx, sess, id, data)
}

// SurveyStreak reports read-only streaks in coordinator sessions. An
// inspect turn's reads are its work, so its streak is not reported.
func (m *Service) SurveyStreak(
	ctx context.Context,
	sess *api.Session,
	batches int,
	tools []string,
) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() || m.ledger.TurnKind(sess.ID) == turnload.KindInspect {
		return promptloop.HostNudge{}
	}
	return m.Render(ctx, sess, anchor.TurnSurveyStreak, map[string]any{
		"batches": batches,
		"tools":   append([]string(nil), tools...),
	})
}

// SecretWithheld renders a withheld-request notice.
func (m *Service) SecretWithheld(
	ctx context.Context,
	sess *api.Session,
	guidanceText string,
) promptloop.HostNudge {
	return m.Render(ctx, sess, anchor.OutboundSecretWithheld, map[string]any{
		"guidance": strings.TrimSpace(guidanceText),
	})
}

func (m *Service) IterationRunway(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	remaining int,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	if sess.IsWorkerChild() {
		return m.WorkerRunway(ctx, sess, remaining)
	}
	return m.Render(ctx, sess, anchor.TurnIterationsLow, map[string]any{
		"remaining": remaining,
	})
}

// WorkerRunway tells any worker its ceiling is near and whether it may ask for more.
func (m *Service) WorkerRunway(ctx context.Context, sess *api.Session, remaining int) promptloop.HostNudge {
	if m.tasks == nil {
		return promptloop.HostNudge{}
	}
	task, ok := m.tasks.Get(workercontext.Job(ctx))
	if !ok || task == nil {
		return promptloop.HostNudge{}
	}
	budget := m.budget.BudgetForTask(ctx, task)
	return m.Render(ctx, sess, anchor.WorkerIterationsLow, map[string]any{
		"remaining":      remaining,
		"max_tool_loops": task.MaxToolLoops,
		"at_host_max":    task.MaxToolLoops >= budget.Max,
		"request_open":   task.BudgetRequest != nil,
	})
}

// BudgetAnswer renders how the coordinator answered a worker's
// budget: worker.budget.raised or worker.budget.declined.
func (m *Service) BudgetAnswer(id anchor.ID) func(context.Context, *api.Session, int, int) promptloop.HostNudge {
	return func(ctx context.Context, sess *api.Session, used, max int) promptloop.HostNudge {
		if m == nil || sess == nil || !sess.IsWorkerChild() {
			return promptloop.HostNudge{}
		}
		return m.Render(ctx, sess, id, map[string]any{
			"tool_loops_used": used,
			"max_tool_loops":  max,
			"remaining":       max - used,
		})
	}
}

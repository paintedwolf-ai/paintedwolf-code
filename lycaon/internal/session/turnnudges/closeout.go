package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
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
		return promptloop.HostNudge{}
	}
	return m.Render(ctx, sess, anchor.TurnIterationsLow, map[string]any{
		"remaining": remaining,
	})
}

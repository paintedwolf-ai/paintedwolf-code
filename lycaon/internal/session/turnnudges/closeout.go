package turnnudges

import (
	"context"
	"log/slog"
	"strings"

	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/pkg/api"
)

// RenderWithReader resolves session anchor bindings against an explicit Reader.
func (m *Service) RenderWithReader(
	ctx context.Context,
	sess *api.Session,
	reader prompts.Reader,
	id anchor.ID,
	data map[string]any,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	surf := "coordinator"
	if sess.IsWorkerChild() {
		surf = "worker"
	}
	stem := anchor.InformRenderFor(ctx, id, anchor.MatchContext{Surface: surf, SessionID: sess.ID})
	if stem == "" {
		return promptloop.HostNudge{}
	}
	return promptloop.HostNudge{
		Content:  workercloseout.RenderWorkerKick(ctx, m.renderKick, reader, stem, data),
		SignalID: string(id),
	}
}

func (m *Service) Closeout(
	ctx context.Context,
	sess *api.Session,
	reader prompts.Reader,
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
		} else {
			data["leg_command_mutation_capable"] = commandMutationCapable(sess, reader)
		}
		// The closeout request offers only the report tool.
		closeoutReader, err := reader.Restricted([]string{workertools.CompleteLegTool})
		if err != nil {
			slog.WarnContext(ctx, "worker closeout reader", "session_id", sess.ID, "error", err)
		}
		return m.RenderWithReader(ctx, sess, closeoutReader, id, data)
	}
	surfaceID := ""
	if rt := m.surface; rt != nil {
		surfaceID = rt.PromptTurnSurfaceID(sess.ID)
	}
	id := anchor.TurnCloseout
	if surface.SurfaceDeliversReport(surfaceID) {
		id = anchor.CoordinatorCloseout
	} else {
		data["session_command_mutation_capable"] = commandMutationCapable(sess, reader)
	}
	// The closeout request offers no tools.
	closeoutReader, err := reader.Restricted(nil)
	if err != nil {
		slog.WarnContext(ctx, "closeout reader", "session_id", sess.ID, "error", err)
	}
	return m.RenderWithReader(ctx, sess, closeoutReader, id, data)
}

// SurveyStreak reports read-only streaks in coordinator sessions. An
// inspect turn's reads are its work, so its streak is not reported.
func (m *Service) SurveyStreak(
	ctx context.Context,
	sess *api.Session,
	reader prompts.Reader,
	batches int,
	tools []string,
) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() || m.ledger.TurnKind(sess.ID) == turnload.KindInspect {
		return promptloop.HostNudge{}
	}
	return m.RenderWithReader(ctx, sess, reader, anchor.TurnSurveyStreak, map[string]any{
		"batches": batches,
		"tools":   append([]string(nil), tools...),
	})
}

// SecretWithheld renders a withheld-request notice.
func (m *Service) SecretWithheld(
	ctx context.Context,
	sess *api.Session,
	reader prompts.Reader,
	guidanceText string,
) promptloop.HostNudge {
	return m.RenderWithReader(ctx, sess, reader, anchor.OutboundSecretWithheld, map[string]any{
		"guidance": strings.TrimSpace(guidanceText),
	})
}

func (m *Service) IterationRunway(
	ctx context.Context,
	sess *api.Session,
	reader prompts.Reader,
	remaining int,
) promptloop.HostNudge {
	if m == nil || sess == nil {
		return promptloop.HostNudge{}
	}
	if sess.IsWorkerChild() {
		return promptloop.HostNudge{}
	}
	return m.RenderWithReader(ctx, sess, reader, anchor.TurnIterationsLow, map[string]any{
		"remaining": remaining,
	})
}

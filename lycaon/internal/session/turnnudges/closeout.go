package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
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
	return m.RenderWithReader(ctx, sess, m.activeTurnReader(ctx, sess, ""), id, data)
}

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
		} else {
			data["leg_command_mutation_capable"] = m.commandMutationCapable(ctx, sess, profileID)
		}
		return m.RenderWithReader(ctx, sess, m.closeoutReader(ctx, sess, profileID, true), id, data)
	}
	surfaceID := ""
	if rt := m.surface; rt != nil {
		surfaceID = rt.PromptTurnSurfaceID(sess.ID)
	}
	id := anchor.TurnCloseout
	if surface.SurfaceDeliversReport(surfaceID) {
		id = anchor.CoordinatorCloseout
	} else {
		data["session_command_mutation_capable"] = m.commandMutationCapable(ctx, sess, profileID)
	}
	return m.RenderWithReader(ctx, sess, m.closeoutReader(ctx, sess, profileID, false), id, data)
}

func (m *Service) closeoutReader(ctx context.Context, sess *api.Session, explicitProfileID string, worker bool) prompts.Reader {
	profiles := m.sessionProfiles(ctx, sess)
	if len(profiles) == 0 {
		return prompts.Reader{}
	}
	profileID := m.resolveProfileID(ctx, sess, explicitProfileID)
	facts := m.resolveHostFacts(ctx, sess)
	sessionMode := string(api.SessionModeAgent)
	if sess != nil && sess.Mode == api.SessionModeAsk {
		sessionMode = string(api.SessionModeAsk)
	}
	visible := []string{}
	if worker {
		visible = []string{"complete_leg"}
	}
	r, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Visible:               visible,
		RootCount:             facts.rootCount,
		HasGit:                facts.hasGit,
		HasAnyGitRoot:         facts.hasAnyGitRoot,
		OnWorkerBranch:        facts.onWorkerBranch,
		WebSearch:             facts.webSearch,
		ReviewsConfinedWrites: facts.reviewsConfinedWrites,
		CommandExternalHidden: facts.externalHidden,
		SessionScratchDir:     facts.scratchDir,
		SharedTempLocations:   facts.sharedTemp,
	})
	if err != nil {
		return prompts.Reader{}
	}
	return r
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
	return m.RenderWithReader(ctx, sess, m.activeTurnReader(ctx, sess, profileID), anchor.TurnIterationsLow, map[string]any{
		"remaining": remaining,
	})
}

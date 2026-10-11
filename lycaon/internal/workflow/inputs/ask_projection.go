package inputs

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// ReconcileCoordinatorAskProjection restores ask transcript projections.
func (m *Asks) ReconcileCoordinatorAskProjection(ctx context.Context, sessionID string, vars map[string]any) {
	if m == nil || m.Sessions == nil {
		return
	}
	ask, ok := runstate.CoordinatorAskFromVars(vars)
	if !ok || (ask.State != runstate.CoordinatorAskPending && ask.State != runstate.CoordinatorAskAnswered) {
		return
	}
	run, err := m.Runs.Get(ctx, ask.RunID)
	if err != nil || run == nil {
		return
	}
	fb := &workflowdef.UserFeedbackPrompt{
		Prompt: ask.Prompt, ResponseType: ask.ResponseType, Options: append([]string(nil), ask.Options...),
		AllowOther: ask.AllowOther, ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
		Secret: ask.Secret,
	}
	_, card := runstate.BuildFeedbackAnnouncement(run, ask.ID, fb, vars, runstate.AnnouncementMessageID(run.ID, ask.ID))
	if card != nil {
		msgs, messagesErr := m.Sessions.GetMessages(ctx, sessionID)
		if messagesErr == nil {
			found := false
			for _, msg := range msgs {
				if msg.ID == card.ID {
					found = true
					break
				}
			}
			if !found {
				m.Cards.AppendAnnouncement(ctx, sessionID, card)
			}
		}
	}
	if ask.State == runstate.CoordinatorAskAnswered {
		m.Cards.PersistCoordinatorAskAnswer(ctx, sessionID, ask)
		m.Cards.StampFeedbackAnswer(ctx, sessionID, ask.RunID, ask.ID, ask.Response, ask.ResolvedByPersonID)
	}
}

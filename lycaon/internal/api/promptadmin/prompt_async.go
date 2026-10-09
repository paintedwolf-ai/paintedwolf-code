package promptadmin

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func renderPromptHostError(catalog *usernotice.Catalog, err error, hasProgress bool) wire.SessionHostError {
	code := promptHostErrorCode(err)
	host := wire.NewSessionHostError(code)
	if catalog == nil {
		return host
	}
	ctx := usernotice.ContextFromPromptError(err)
	if ctx == nil {
		ctx = make(map[string]any)
	}
	ctx[usernotice.ContextTurnProgress] = usernotice.TurnProgressNone
	if hasProgress {
		ctx[usernotice.ContextTurnProgress] = usernotice.TurnProgressMade
	}
	copy := catalog.RenderWire(string(code), ctx)
	host.Title = copy.Title
	host.Message = copy.Message
	host.SuggestedAction = copy.SuggestedAction
	// Each code's catalog entry owns its actions; informational notices carry none.
	for _, action := range copy.Actions {
		host.Actions = append(host.Actions, wire.NoticeAction(action))
	}
	// Placement controls notice scope and deduplication.
	if placement, ok := catalog.Placement(string(code), ctx); ok {
		host.Tier = wire.NoticeTier(placement.Tier)
		host.Scope = wire.NoticeScope(placement.Scope)
		host.Resolution = placement.ID
	}
	return host
}

func promptHostErrorCode(err error) wire.NoticeCode {
	if err == nil {
		return wire.NoticeCodePromptFailed
	}
	if code, ok := noticeerr.CodeOf(err); ok {
		return code
	}
	// Transport reachability is a predicate over the chain, not one error type.
	if httpclient.Unreachable(err) {
		return wire.NoticeCodeProviderUnreachable
	}
	return wire.NoticeCodePromptFailed
}

func shouldPublishPromptHostError(err error) bool {
	if err == nil {
		return false
	}
	// Stopped and active submissions are not host errors.
	return !errors.Is(err, lifecycle.ErrStopping) &&
		!errors.Is(err, session.ErrPromptSubmissionInFlight)
}

// Compile-time check for the host-turn failure callback.
var _ session.TurnFailureSink = (*Execution)(nil).PublishTurnFailure

func (s *Execution) PublishTurnFailure(ctx context.Context, sessionID string, err error) {
	err = session.UnreportedTurnFailure(err)
	if !shouldPublishPromptHostError(err) {
		return
	}
	sess, getErr := s.Store.Get(ctx, sessionID)
	if getErr != nil {
		return
	}
	projectKey := sess.ProjectID
	hostErr := renderPromptHostError(s.responses.Notices, err, failedTurnProgress(ctx, s.Store, sessionID))
	s.EventPublisher.PublishSessionHostError(ctx, projectKey, sessionID, hostErr)
}

// failedTurnProgress reports whether a failed turn may have produced effects. A
// transcript that cannot be read may hold tool effects, so only a read
// transcript can rule progress out and offer a plain retry.
func failedTurnProgress(ctx context.Context, st session.Store, sessionID string) bool {
	msgs, err := st.GetMessages(ctx, sessionID)
	if err != nil {
		return true
	}
	return sessionMessagesHaveTurnProgress(msgs)
}

func sessionMessagesHaveTurnProgress(msgs []wire.Message) bool {
	lastAskIdx := wire.UserIntentBoundary(msgs) - 1
	if lastAskIdx == -1 {
		return false
	}
	for i := lastAskIdx + 1; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == wire.MessageRoleAssistant || m.Role == wire.MessageRoleTool || len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func (s *Execution) ResumePromptSubmission(parent context.Context, sessionID string, row *store.PromptSubmission) {
	if row == nil || row.Status != store.PromptSubmissionQueued {
		return
	}
	s.runPromptAsync(parent, sessionID, row.ID)
}

func (s *Execution) runPromptAsync(parent context.Context, sessionID, submissionID string) {
	s.background.Go(parent, func(ctx context.Context) {
		perf := observability.StartPerformanceOperation("prompt.run", map[string]string{
			"session_id": sessionID, "operation_id": submissionID,
		})
		outcome := "error"
		defer func() { perf.End(outcome) }()
		retentions, retentionErr := s.Attachments.capturePromptAttachmentRetentions(ctx, submissionID)
		perf.Mark("retain_attachments")
		var err error
		if retentionErr == nil {
			_, err = s.Sessions.RunPromptSubmission(ctx, submissionID)
		} else {
			err = retentionErr
		}
		perf.Mark("execute")
		cleanupCtx := context.WithoutCancel(ctx)
		if retentionErr != nil {
			if s.responses.Logger != nil {
				s.responses.Logger.WarnContext(cleanupCtx, "capture prompt attachment retentions", "operation_id", submissionID, "error", retentionErr)
			}
			// A receipt left queued would pend forever.
			if abandonErr := s.Sessions.AbandonPromptSubmission(cleanupCtx, submissionID, retentionErr); abandonErr != nil && s.responses.Logger != nil {
				s.responses.Logger.WarnContext(cleanupCtx, "abandon unrunnable prompt submission", "operation_id", submissionID, "error", abandonErr)
			}
		} else if cleanupErr := s.Attachments.reconcilePromptAttachmentRetentions(cleanupCtx, retentions); cleanupErr != nil && s.responses.Logger != nil {
			s.responses.Logger.WarnContext(cleanupCtx, "reconcile prompt attachment retentions", "operation_id", submissionID, "error", cleanupErr)
		}
		if err != nil {
			s.PublishTurnFailure(ctx, sessionID, err)
			if errors.Is(err, context.Canceled) {
				outcome = "canceled"
			}
			return
		}
		outcome = "ok"
	})
}

// RecoverPromptSubmissions requeues pending receipts in submission order.
func (s *Execution) RecoverPromptSubmissions(ctx context.Context) error {
	ids, err := s.Sessions.RecoverPromptSubmissions(ctx)
	if err != nil {
		return err
	}
	drainSessions := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	retentionsBySession := make(map[string][]promptAttachmentRetentionSet)
	for _, id := range ids {
		row, getErr := s.Sessions.GetPromptSubmission(ctx, id)
		if getErr != nil {
			return getErr
		}
		retentions, retentionErr := s.Attachments.capturePromptAttachmentRetentions(ctx, id)
		if retentionErr != nil {
			return retentionErr
		}
		retentionsBySession[row.SessionID] = append(retentionsBySession[row.SessionID], retentions)
		if _, ok := seen[row.SessionID]; !ok {
			seen[row.SessionID] = struct{}{}
			drainSessions = append(drainSessions, row.SessionID)
		}
	}
	for _, sessionID := range drainSessions {
		retentionSets := retentionsBySession[sessionID]
		s.background.Go(ctx, func(drainCtx context.Context) {
			if err := s.Sessions.DrainPromptSubmissions(drainCtx, sessionID); err != nil {
				s.PublishTurnFailure(drainCtx, sessionID, err)
			}
			cleanupCtx := context.WithoutCancel(drainCtx)
			for _, retentions := range retentionSets {
				if err := s.Attachments.reconcilePromptAttachmentRetentions(cleanupCtx, retentions); err != nil && s.responses.Logger != nil {
					s.responses.Logger.WarnContext(cleanupCtx, "reconcile recovered prompt attachments", "operation_id", retentions.operationID, "error", err)
				}
			}
		})
	}
	return nil
}

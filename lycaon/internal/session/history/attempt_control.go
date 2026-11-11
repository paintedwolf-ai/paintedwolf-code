package history

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

// attemptController decides whether a background page runs and settles what it found.
// Admission and settlement run inside Runner.Execute, so they never overlap per session.
type attemptController struct {
	m      *Service
	ledger *attemptLedger
}

func (m *Service) attemptControl() attemptController {
	return attemptController{m: m, ledger: m.Runner.attempts}
}

// capture reads the last row and the mutation clock together. A row appended after
// this is seen by the scan yet still counts as new, which at worst repeats one attempt.
func (c attemptController) capture(ctx context.Context, sess *api.Session, sc *compaction.SimpleCompactor) (attemptPass, error) {
	tail, err := c.m.store.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 1})
	if err != nil {
		return attemptPass{}, err
	}
	pass := attemptPass{source: attemptSource{seq: tail.Watermark}}
	if n := len(tail.Messages); n > 0 {
		pass.source.ord, pass.source.id = tail.Messages[n-1].Ord, tail.Messages[n-1].ID
	}
	pass.key, err = c.key(ctx, sess, sc)
	return pass, err
}

func (c attemptController) key(ctx context.Context, sess *api.Session, sc *compaction.SimpleCompactor) (string, error) {
	info := compaction.SessionInfo{}
	if c.m.Workspace != nil {
		info.SettingsRoots = c.m.Workspace.SettingsRoots(ctx, sess)
	}
	revision, err := compaction.AttemptRevision(ctx, sc.WithConfig(c.m.compactionConfig(ctx, sess)), info)
	if err != nil {
		return "", err
	}
	provider, model := c.m.Model(ctx, sess)
	return compaction.RevisionDigest(struct {
		Generation            int
		Provider, Model, Rule string
	}{sess.CompactionGeneration, provider, model, revision})
}

// admit runs before any candidate scan. Store reads here are bounded by one row
// plus attemptResumeRows, and a failed read admits the pass rather than stalling it.
func (c attemptController) admit(ctx context.Context, sessionID string, sc *compaction.SimpleCompactor) (attemptPass, bool) {
	sess, err := c.m.store.Get(ctx, sessionID)
	if err != nil {
		return attemptPass{}, true
	}
	pass, err := c.capture(ctx, sess, sc)
	if err != nil {
		return attemptPass{}, true
	}
	prior, found := c.ledger.records.Load(sessionID)
	if !found || prior.key != pass.key {
		return pass, true
	}
	current, err := c.m.store.CompactionViewCurrent(ctx, sessionID, prior.source.ord, prior.source.id, prior.source.seq)
	if err != nil || !current {
		return pass, true
	}
	appended, err := c.m.store.ReadMessageWindow(ctx, sessionID, messageview.MessageWindow{After: true, AfterOrd: prior.source.ord, Limit: attemptResumeRows})
	if err != nil {
		return pass, true
	}
	progress := len(appended) >= attemptResumeRows || hasNewIntent(appended, prior.intentID)
	if prior.outcome == attemptTransient && prior.retries < maxTransientRetries {
		if c.ledger.clock.Now().Before(prior.retryAt) {
			return pass, false
		}
		if !progress {
			pass.retries = prior.retries + 1
		}
		return pass, true
	}
	if !progress {
		slog.DebugContext(ctx, "compaction attempt suppressed", "session_id", sessionID, "outcome", prior.outcome, "reason", prior.reason, "new_rows", len(appended))
	}
	return pass, progress
}

func hasNewIntent(rows []api.Message, attempted string) bool {
	for _, row := range rows {
		if api.IsUserIntentMessage(row) && row.ID != attempted {
			return true
		}
	}
	return false
}

// explicit identifies a synchronous request, which skips admission. Its failure
// starts a new retry budget.
func (c attemptController) explicit(ctx context.Context, sess *api.Session, sc *compaction.SimpleCompactor) attemptPass {
	pass, err := c.capture(ctx, sess, sc)
	if err != nil {
		return attemptPass{explicit: true}
	}
	pass.explicit = true
	return pass
}

// attemptResult is one settled outcome. retryAfter is a provider's declared wait.
type attemptResult struct {
	outcome             attemptOutcome
	reason              string
	retryAfter          time.Duration
	candidateThroughOrd int64
	intentID            string
	memo                *messageview.CompactionAttempt
}

// settlePage records what a deterministic page found. A rejected page awaits its
// synthesis, and a page that needed no compaction ends any pending backoff.
func (c attemptController) settlePage(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, pass attemptPass, needed bool, report compaction.CompactionReport, compactErr error, candidateThroughOrd int64) {
	if !needed {
		if prior, found := c.ledger.records.Load(info.ID); found && prior.outcome == attemptTransient {
			c.ledger.forget(info.ID)
		}
		return
	}
	if compactErr == nil && report.Reason == "insufficient_savings" {
		return
	}
	result := attemptResult{outcome: attemptNoReduction, reason: report.Reason, candidateThroughOrd: candidateThroughOrd, intentID: info.LatestUserRequestID}
	if compactErr != nil {
		result.outcome = classifyAttemptFailure(compactErr)
		result.retryAfter = failure.DeclaredRetryAfter(compactErr)
		logAttemptFailure(ctx, info.ID, result, compactErr)
	}
	c.settle(ctx, sc, info.ID, pass, result)
}

// settleSynthesis records the outcome of a summarizer attempt that published nothing.
func (c attemptController) settleSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, pass attemptPass, report compaction.CompactionReport, compactErr error, candidateThroughOrd int64, memo *messageview.CompactionAttempt) {
	result := attemptResult{outcome: attemptNoReduction, reason: report.Reason, candidateThroughOrd: candidateThroughOrd, intentID: info.LatestUserRequestID, memo: memo}
	failed := compactErr
	if failed == nil {
		failed = report.SummaryFailure
	}
	if failed != nil {
		result.outcome = classifyAttemptFailure(failed)
		result.retryAfter = failure.DeclaredRetryAfter(failed)
		logAttemptFailure(ctx, info.ID, result, failed)
	}
	c.settle(ctx, sc, info.ID, pass, result)
}

func logAttemptFailure(ctx context.Context, sessionID string, result attemptResult, err error) {
	slog.InfoContext(ctx, "compaction attempt failed", "session_id", sessionID,
		"outcome", result.outcome, "failure_type", fmt.Sprintf("%T", err), "retry_after", result.retryAfter)
}

// settle is the fence: canceled and superseded work changes nothing. A newer
// generation, configuration, edit, or later attempt keeps its own record.
func (c attemptController) settle(ctx context.Context, sc *compaction.SimpleCompactor, sessionID string, pass attemptPass, result attemptResult) {
	if ctx.Err() != nil {
		return
	}
	if pass.key == "" {
		c.saveMemo(ctx, sessionID, result.memo)
		return
	}
	sess, err := c.m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	key, err := c.key(ctx, sess, sc)
	if err != nil || key != pass.key {
		return
	}
	current, err := c.m.store.CompactionViewCurrent(ctx, sessionID, pass.source.ord, pass.source.id, pass.source.seq)
	if err != nil || !current {
		return
	}
	prior, found := c.ledger.records.Load(sessionID)
	if found && !pass.explicit && prior.key == pass.key && prior.source.seq > pass.source.seq {
		return
	}
	c.saveMemo(ctx, sessionID, result.memo)
	record := attemptRecord{key: pass.key, source: pass.source, candidateThroughOrd: result.candidateThroughOrd,
		intentID: result.intentID, outcome: result.outcome, reason: result.reason, retries: pass.retries}
	c.ledger.disarm(sessionID)
	if result.outcome == attemptTransient && pass.retries < maxTransientRetries {
		delay := max(transientRetryDelays[pass.retries], result.retryAfter)
		record.retryAt = c.ledger.clock.Now().Add(delay)
		c.ledger.arm(sessionID, delay, func() { c.fire(sessionID) }) //nolint:contextcheck // the retry fires after this pass's context ends.
	}
	c.ledger.records.Store(sessionID, record)
}

func (c attemptController) saveMemo(ctx context.Context, sessionID string, memo *messageview.CompactionAttempt) {
	if memo == nil {
		return
	}
	if err := c.m.store.PutCompactionAttempt(ctx, sessionID, *memo); err != nil {
		slog.WarnContext(ctx, "compaction attempt memo save failed", "session_id", sessionID, "error", err)
	}
}

// deferRetry keeps a due retry alive when synthesis admission declined it, without
// spending budget. Declined work made no provider call.
func (c attemptController) deferRetry(sessionID string, pass attemptPass) {
	if pass.retries > 0 {
		c.ledger.arm(sessionID, transientRetryDelays[pass.retries-1], func() { c.fire(sessionID) })
	}
}

// fire needs no transcript append: the pass re-evaluates whether compaction is
// still needed, and admission finds the retry due.
func (c attemptController) fire(sessionID string) {
	defer observability.GuardPanic("session.compaction_retry")
	ctx, finish, err := c.m.Runner.work.Begin(context.Background())
	if err != nil {
		return
	}
	defer finish()
	sess, err := c.m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	c.m.Schedule(ctx, sess)
}

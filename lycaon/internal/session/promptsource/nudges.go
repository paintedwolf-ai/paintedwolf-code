package promptsource

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/cost"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerProgressPublisher interface {
	PublishWorkerProgress(context.Context, string, workerprogress.Snapshot, bool) error
}

type Nudges struct {
	DoomLoop loopguard.DoomLoopGuard
	Nudges   *turnnudges.Service
	Policy   *policyfacts.Service
	Spend    *spendguard.Service
	Workers  workeroutcomes.CycleLedger
	// Rounds bounds tool-less worker responses and sets round notice thresholds.
	Rounds spawn.RoundPolicy
	// RoundUsage measures a worker round's tokens and spend.
	RoundUsage RoundUsage
	// RoundNotice delivers a round notice to the worker's coordinator.
	RoundNotice func(ctx context.Context, parentID string, facts kick.WorkerRoundNoticeFacts)
}

// RoundUsage sums one session's model calls since a time.
type RoundUsage interface {
	SessionUsageSince(ctx context.Context, sessionID string, since time.Time) (cost.SessionUsage, error)
}

func (m *Nudges) Build() promptloop.NudgesDeps {
	deps := promptloop.NudgesDeps{
		DoomLoop: m.DoomLoop,
		FormatDoomLoopReject: func(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
			return m.Policy.FormatDoomLoopReject(ctx, sessionID, tool, args, count, repeatedCode)
		},
		EscalateRepeatedCode: func(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
			total := m.Policy.CodeRejectResponses(sessionID, tool, original.Code())
			if total < loopguard.DoomLoopMaxCodeRepeats {
				return nil
			}
			return m.Policy.EscalateRepeatedCode(ctx, sessionID, tool, original, total)
		},
	}
	deps.CheckSpendCeiling = func(ctx context.Context, sessionID string, sess *api.Session) (promptloop.SpendCeilingCheck, error) {
		st, err := m.Spend.State(ctx, sessionID, sess)
		if err != nil {
			slog.WarnContext(ctx, "check spend ceiling", "session_id", sessionID, "error", err)
			return promptloop.SpendCeilingCheck{}, nil
		}
		if st.Reached {
			return promptloop.SpendCeilingCheck{SoftStop: st.SoftStop}, st.ReachedError()
		}
		return promptloop.SpendCeilingCheck{
			Runway: promptloop.SpendRunway{Low: st.Low, CeilingUSD: st.CeilingUSD},
		}, nil
	}
	deps.IsSpendCeiling = func(err error) bool { return errors.Is(err, spendguard.ErrCeiling) }
	deps.SecretWithheldNudge = m.Nudges.SecretWithheld
	deps.SurveyStreakNudge = m.Nudges.SurveyStreak
	deps.SpendRunwayNudge = m.Nudges.SpendRunway
	deps.SpendSoftStopNudge = m.Nudges.SpendSoftStop
	deps.PublishWorkerProgress = m.PublishWorkerProgress
	deps.WorkerJob = m.WorkerJob
	deps.WorkerRounds = m.Rounds
	deps.WorkerRoundConsumption = m.consumptionNotice

	return deps
}

func (m *Nudges) PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) {
	if m == nil || strings.TrimSpace(workerJobID) == "" {
		return
	}
	pub, ok := m.Workers.(workerProgressPublisher)
	if !ok || pub == nil {
		return
	}
	_ = pub.PublishWorkerProgress(ctx, workerJobID, snap, checkpoint)
}

func (m *Nudges) WorkerJob(_ context.Context, workerJobID string) (*api.WorkerTask, bool) {
	if m == nil || m.Workers == nil {
		return nil, false
	}
	return m.Workers.Get(strings.TrimSpace(workerJobID))
}

// consumptionNotice measures a worker round and tells its coordinator.
func (m *Nudges) consumptionNotice(ctx context.Context, sess *api.Session, workerJobID string, modelCalls int) {
	if m == nil || m.RoundNotice == nil || sess == nil {
		return
	}
	task, ok := m.WorkerJob(ctx, workerJobID)
	if !ok || task == nil {
		return
	}
	facts := kick.WorkerRoundNoticeFacts{JobID: task.ID, AgentType: task.AgentType, Kind: kick.RoundNoticeConsumption, ModelCalls: modelCalls}
	if since := roundStart(task); m.RoundUsage != nil && !since.IsZero() {
		if usage, err := m.RoundUsage.SessionUsageSince(ctx, sess.ID, since); err == nil {
			facts.Tokens, facts.SpendNanoUSD, facts.SpendPriced = usage.Tokens, usage.SpendNanoUSD, usage.Priced
		} else {
			slog.WarnContext(ctx, "measure worker round", "worker_job_id", task.ID, "error", err)
		}
	}
	m.RoundNotice(ctx, sess.ParentSessionID, facts)
}

// roundStart is when the job began running, or when it was created.
func roundStart(task *api.WorkerTask) time.Time {
	if task.StartedAt != nil {
		return *task.StartedAt
	}
	return task.CreatedAt
}

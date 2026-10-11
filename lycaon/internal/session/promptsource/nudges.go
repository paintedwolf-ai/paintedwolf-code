package promptsource

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
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
	deps.WorkerBudgetRaisedNudge = m.Nudges.BudgetAnswer(anchor.WorkerBudgetRaised)
	deps.WorkerBudgetDeclinedNudge = m.Nudges.BudgetAnswer(anchor.WorkerBudgetDeclined)
	deps.WorkerBudgetAnswerWait = spawn.WorkerBudgetAnswerWait
	deps.SurveyStreakNudge = m.Nudges.SurveyStreak
	deps.SpendRunwayNudge = m.Nudges.SpendRunway
	deps.SpendSoftStopNudge = m.Nudges.SpendSoftStop
	deps.PublishWorkerProgress = m.PublishWorkerProgress
	deps.WorkerJob = m.WorkerJob

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

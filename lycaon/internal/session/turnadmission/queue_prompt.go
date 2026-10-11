package turnadmission

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
)

func (m *Service) RunLocked(ctx context.Context, id string, in promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
	turn, err := m.Gate.Capture(ctx, id)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if err := m.Workspace.CheckReady(ctx, id); err != nil {
		lock.Unlock()
		return nil, err
	}
	return m.runTurnAndDrain(ctx, id, in, lock, turn)
}

func (m *Service) runTurnAndDrain(ctx context.Context, id string, in promptinput.Input, lock *promptstate.Lock, turn lifecycle.Turn) (*promptresult.Result, error) {
	resp, err := func() (*promptresult.Result, error) {
		defer lock.Unlock()
		return m.Runner.Run(ctx, id, in)
	}()
	LogTurnFailure(ctx, id, err)
	err = m.Turns.ReportFailure(ctx, id, err)
	if m.Gate.MayDrain(turn) {
		if drainErr := m.Settlement.Drain(ctx, id); drainErr != nil {
			err = errors.Join(err, drainErr)
		}
		m.MaybePromote(ctx, id)
	}
	// Drain a Send reserved after the loop's final boundary check.
	if m.queue != nil && m.queue.Snapshot(id).Sending {
		if drainErr := m.Submissions.DrainPromptSubmissions(context.WithoutCancel(ctx), id); drainErr != nil {
			err = errors.Join(err, drainErr)
		}
	}
	return resp, err
}

func LogTurnFailure(ctx context.Context, sessionID string, err error) {
	if err == nil || errors.Is(err, lifecycle.ErrStopping) {
		return
	}
	slog.ErrorContext(ctx, "coordinator turn failed", "session_id", sessionID, "err", err)
}

func (m *Service) RoundComplete(ctx context.Context, sessionID string) bool {
	if m == nil {
		return true
	}
	nudges, cycles := m.nudges, m.cycles
	if nudges == nil || cycles == nil {
		return true
	}
	if nudges.HasPendingLoopWakes(sessionID) && !m.Turns.HostTurnBlocked(ctx, sessionID) {
		return false
	}
	return cycles.WorkerCycleIsIdle(ctx, sessionID)
}

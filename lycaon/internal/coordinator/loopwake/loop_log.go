package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"log/slog"
	"time"
)

const loopLogComponent = "coordinator_loop"

func loopLogNudge(sessionID string, wake, inform anchor.ID, legID, completingJobID, outcome string) {
	slog.Debug("loop nudge",
		"component", loopLogComponent,
		"session_id", sessionID,
		"wake", string(wake),
		"inform", string(inform),
		"leg_id", legID,
		"completing_job_id", completingJobID,
		"outcome", outcome,
	)
}

func loopLogSleep(sessionID, action, reason string, until time.Time) {
	attrs := []any{
		"component", loopLogComponent,
		"session_id", sessionID,
		"action", action,
		"reason", reason,
	}
	if !until.IsZero() {
		attrs = append(attrs, "until", until.UTC().Format(time.RFC3339))
	}
	slog.Debug("loop sleep", attrs...)
}

func loopLogBudget(sessionID, runID string, consumed, max int, allowed bool) {
	slog.Debug("loop budget",
		"component", loopLogComponent,
		"session_id", sessionID,
		"run_id", runID,
		"consumed", consumed,
		"max", max,
		"allowed", allowed,
	)
}

func loopLogRunPrompt(sessionID string, wake anchor.ID) {
	slog.Debug("loop run prompt",
		"component", loopLogComponent,
		"session_id", sessionID,
		"wake", string(wake),
	)
}

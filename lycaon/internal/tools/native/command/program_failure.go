package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
)

func beginProgramAttempt(ctx context.Context, ft *CommandFailureTracker, tc tools.ToolContext, args map[string]any, cwd string) (*commandFailureAttempt, func() commandSourceIdentity, error) {
	observeSource, reader := commandSourceReaders(ctx, tc, cwd)
	attempt, reject := ft.begin(tc.Identity.SessionID, commandFailureApproachKey(canonicalToolArgs(tc, args)), CanonicalCommandKey(tc, args), observeSource)
	if reject != nil {
		return nil, nil, reject
	}
	var verification func() commandSourceIdentity
	if tc.Execution.VerificationCheck {
		identity := reader()
		verification = func() commandSourceIdentity { return identity }
	}
	return attempt, verification, nil
}

// recordProgramAttempt records completed non-refused outcomes before call cleanup cancels ctx.
func recordProgramAttempt(ft *CommandFailureTracker, attempt *commandFailureAttempt, ctx context.Context, outcome ProgramOutcome) {
	if ctx.Err() != nil || outcome.State != "completed" || outcome.Refusal != nil {
		return
	}
	ft.record(attempt, outcome.Status == 0)
}

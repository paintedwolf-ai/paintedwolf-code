package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"log/slog"
)

type ScreenAuthority interface {
	ResolveSecretScreenUnasked(context.Context, secretmatch.Alert) (secretmatch.Resolution, error)
	AskSecretScreen(context.Context, secretmatch.Alert) (secretmatch.Resolution, error)
}

func (b *Runtime) Ask(authority ScreenAuthority, disabled func(string) bool) secretmatch.AskFunc {
	return func(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		if authority == nil {
			return secretmatch.Resolution{}, secretmatch.NewAskFault(
				secretmatch.FaultStageScreenUnwired, nil)
		}
		// Recorded redactions also apply when approvals are disabled.
		if disabled(secretmatch.AskAttributionFrom(ctx).ProjectDir) {
			return authority.ResolveSecretScreenUnasked(ctx, finding)
		}
		resolution, err := authority.AskSecretScreen(ctx, finding)
		if err != nil {
			// Ask faults have no card or ledger entry.
			slog.ErrorContext(ctx, "outbound secret screen could not ask",
				"component", "secret_screen",
				"surface", string(finding.Surface),
				"session_id", finding.SessionID,
				"root_session_id", finding.RootSessionID,
				"rule_id", finding.RuleID,
				"error", err)
		}
		return resolution, err
	}
}

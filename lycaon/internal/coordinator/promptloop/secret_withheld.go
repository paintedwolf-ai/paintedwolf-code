package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecretWithheldNudge renders credential-release refusals with optional user guidance.
type SecretWithheldNudge func(ctx context.Context, sess *api.Session, guidance string) HostNudge

// maxSecretWithheldRetries allows one recomposition from the redacted transcript.
const maxSecretWithheldRetries = 1

// handleSecretWithheldTurn records the refusal and decides whether to retry.
func (l *turnNudges) handleSecretWithheldTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	cause error,
	st *promptLoopTurnState,
) (retry bool, err error) {
	if st == nil {
		return false, nil
	}
	var nudge HostNudge
	if l.Deps.SecretWithheldNudge != nil {
		nudge = l.Deps.SecretWithheldNudge(ctx, sess, llm.SecretWithheldGuidance(cause))
	}
	if !nudge.Empty() {
		st.history, err = l.appendHostNudge(ctx, sessionID, st.history, nudge, "", st)
		if err != nil {
			return false, err
		}
	}
	if st.secretWithheldRetries >= maxSecretWithheldRetries {
		return false, nil
	}
	st.secretWithheldRetries++
	return true, nil
}

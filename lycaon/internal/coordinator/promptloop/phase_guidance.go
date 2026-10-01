package promptloop

import "context"

// takePhaseGuidance delivers the guidance of a phase entered since the last
// model call. A phase the coordinator advances with its own tool call is
// taught on the next call of the same turn, not at the next wake.
func (l *PromptLoop) takePhaseGuidance(ctx context.Context, sessionID string, state *promptLoopTurnState) error {
	if l.Deps.TakePhaseGuidance == nil {
		return nil
	}
	messages, err := l.Deps.TakePhaseGuidance(ctx, sessionID)
	if err != nil {
		return err
	}
	state.history = append(state.history, messages...)
	return nil
}

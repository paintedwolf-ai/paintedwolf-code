package promptloop

import "context"

func (l *PromptLoop) takePolicyFeedback(ctx context.Context, sessionID string, state *promptLoopTurnState) error {
	if l.Deps.TakePolicyFeedback == nil {
		return nil
	}
	messages, err := l.Deps.TakePolicyFeedback(ctx, sessionID)
	if err != nil {
		return err
	}
	state.history = append(state.history, messages...)
	return nil
}

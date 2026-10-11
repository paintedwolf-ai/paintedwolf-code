package promptloop

import (
	"context"
	"fmt"
)

// The session owner seals this closeout after post-turn hooks.
func (l *turnCloseout) capturePromptRunCloseout(ctx context.Context, st *promptLoopTurnState, result *PromptRunResult) error {
	if result.LastAssistantID == "" {
		return nil
	}
	for _, message := range st.history {
		if message.ID != result.LastAssistantID {
			continue
		}
		stored, _ := l.Projection.storageSafeMessage(ctx, message)
		result.Closeout = &stored
		return nil
	}
	return fmt.Errorf("terminal assistant message is absent from execution history")
}

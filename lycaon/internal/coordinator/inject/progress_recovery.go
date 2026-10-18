package inject

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// RenderProgressRecoveryBlock describes the immediately preceding repair event.
func RenderProgressRecoveryBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string) (string, error) {
	return anchor.RenderInform(ctx, anchor.InjectProgressRecovery, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, nil)
}

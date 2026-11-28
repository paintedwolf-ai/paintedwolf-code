package guidance

import (
	"context"

	"github.com/lycaon/lycaon/internal/prompts"
)

const (
	CuratorSystemRef = "curator/system"
	CuratorUserRef   = "curator/user"
)

// RenderCuratorSystemPrompt renders the lite-model curator system prompt.
func RenderCuratorSystemPrompt(ctx context.Context) (string, error) {
	return prompts.RenderUtilityPrompt(ctx, CuratorSystemRef, nil)
}

// RenderCuratorUserPrompt renders the curator selection user prompt.
func RenderCuratorUserPrompt(ctx context.Context, data map[string]any) (string, error) {
	return prompts.RenderUtilityPrompt(ctx, CuratorUserRef, data)
}

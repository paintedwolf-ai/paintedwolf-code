package guidance

import (
	"context"
	"fmt"
	"strings"
)

const (
	CuratorSystemRef = "curator/system"
	CuratorUserRef   = "curator/user"
)

// RenderCuratorSystemPrompt renders the lite-model curator system prompt.
func RenderCuratorSystemPrompt(ctx context.Context) (string, error) {
	block, err := RenderGuidance(ctx, CuratorSystemRef, nil)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("curator system prompt rendered empty")
	}
	return block, nil
}

// RenderCuratorUserPrompt renders the curator selection user prompt.
func RenderCuratorUserPrompt(ctx context.Context, data map[string]any) (string, error) {
	block, err := RenderGuidance(ctx, CuratorUserRef, data)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("curator user prompt rendered empty")
	}
	return block, nil
}

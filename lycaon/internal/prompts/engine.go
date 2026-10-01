// Package prompts renders agent system prompt templates from the effective pack catalog.
package prompts

import "context"

// PromptTemplateEngine renders named templates with data.
type PromptTemplateEngine interface {
	Render(ctx context.Context, template string, data map[string]any) (string, error)
	RenderInject(ctx context.Context, ref string, data map[string]any) (string, error)
	RenderGuidance(ctx context.Context, ref string, data map[string]any) (string, error)
	RenderKick(ctx context.Context, name string, data map[string]any) (string, error)
	Register(name string, template string) error
}

package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// GuidanceRenderer renders tool feedback.
type GuidanceRenderer struct {
	engine PromptTemplateEngine
}

// NewGuidanceRenderer wraps a template engine for guidance/*.md renders.
func NewGuidanceRenderer(engine PromptTemplateEngine) *GuidanceRenderer {
	if engine == nil {
		return nil
	}
	return &GuidanceRenderer{engine: engine}
}

// Render executes guidance/{ref}.md with the given DTO map.
func (r *GuidanceRenderer) Render(ctx context.Context, ref string, data map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("guidance renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty guidance ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid guidance ref %q", ref)
	}
	return r.engine.RenderGuidance(ctx, name, data)
}

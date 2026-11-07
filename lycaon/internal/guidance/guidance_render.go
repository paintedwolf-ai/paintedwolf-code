package guidance

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/prompts"
	"strings"
	"sync"
)

var (
	guidanceRendererMu sync.RWMutex
	guidanceRenderer   *prompts.GuidanceRenderer
)

// SetGuidanceRenderer installs the process-wide pongo guidance renderer.
func SetGuidanceRenderer(renderer *prompts.GuidanceRenderer) {
	guidanceRendererMu.Lock()
	guidanceRenderer = renderer
	guidanceRendererMu.Unlock()
}

// RenderGuidance renders the pack guidance template guidance/{ref}.md.
func RenderGuidance(ctx context.Context, ref string, data map[string]any) (string, error) {
	guidanceRendererMu.RLock()
	renderer := guidanceRenderer
	guidanceRendererMu.RUnlock()
	if renderer == nil {
		return "", fmt.Errorf("guidance renderer not configured")
	}
	block, err := renderer.Render(ctx, ref, data)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(block), nil
}

// RenderHintFields renders what/why/fix for a hint from its registered fields.
func RenderHintFields(ctx context.Context, code string, entry HintEntry, data map[string]any, resolve NextStepsResolver) (what, why, fix string) {
	view := NormalizeRejectCodeView(code, entry)
	data = withHintNextSteps(ctx, code, view, data, resolve)
	return renderFieldTemplate(view.What, data),
		renderFieldTemplate(view.Cause, data),
		renderFieldTemplate(view.Fix, data)
}

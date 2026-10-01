package guidance

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/prompts"
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

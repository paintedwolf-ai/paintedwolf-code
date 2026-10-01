package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// BlueprintInjectSentinel is embedded in inject/blueprint.md for dedup policy.
const BlueprintInjectSentinel = "<!-- lycaon-blueprint:v1 -->"

// BlueprintView is parsed frontmatter for runtime inject.
type BlueprintView struct {
	Path        string
	Frontmatter []BlueprintField
}

// BlueprintField is one frontmatter row for template rendering.
type BlueprintField struct {
	Key   string
	Value string
}

// RenderBlueprintInject renders inject/blueprint.md when a manifest declares blueprint.
func RenderBlueprintInject(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, view *BlueprintView) (string, error) {
	if renderer == nil || view == nil || strings.TrimSpace(view.Path) == "" {
		return "", nil
	}
	rows := make([]map[string]any, 0, len(view.Frontmatter))
	for _, f := range view.Frontmatter {
		rows = append(rows, map[string]any{"key": f.Key, "value": f.Value})
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectBlueprint, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, map[string]any{
		"blueprint_path": strings.TrimSpace(view.Path),
		"frontmatter":    rows,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, BlueprintInjectSentinel) {
		return "", fmt.Errorf("blueprint inject missing sentinel %q", BlueprintInjectSentinel)
	}
	return block, nil
}

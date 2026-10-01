package anchor

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
)

// ResolveInformRender returns Binding.render for id (fail-closed).
func ResolveInformRender(ctx context.Context, id ID, match MatchContext) (string, error) {
	reg := RegistryFor(ctx, match.SessionID)
	if reg == nil {
		return "", fmt.Errorf("anchor: Binding registry not installed")
	}
	b, ok := reg.ResolveInform(id, match)
	if !ok || b == nil {
		return "", fmt.Errorf("anchor: no inform Binding for %s", id)
	}
	stem := strings.TrimSpace(b.Render)
	if stem == "" {
		return "", fmt.Errorf("anchor: empty render for Binding on %s", id)
	}
	return stem, nil
}

// InformRenderFor resolves a matching synchronous render stem.
func InformRenderFor(ctx context.Context, id ID, match MatchContext) string {
	reg := RegistryFor(ctx, match.SessionID)
	if reg == nil {
		return ""
	}
	b, ok := reg.ResolveInform(id, match)
	if !ok || b == nil {
		return ""
	}
	gc := oar.NewGuardContext()
	gc.Phase = match.Phase
	gc.Surface = match.Surface
	gc.Profile = match.Profile
	gc.SessionPosture = match.SessionPosture
	if !reg.WhenMatches(b, gc) {
		return ""
	}
	return strings.TrimSpace(b.Render)
}

// RenderInform resolves and renders an inform binding.
func RenderInform(
	ctx context.Context,
	id ID,
	match MatchContext,
	inj *prompts.InjectRenderer,
	vars map[string]any,
) (string, error) {
	if inj == nil {
		return "", fmt.Errorf("anchor: inject renderer not configured")
	}
	stem, err := ResolveInformRender(ctx, id, match)
	if err != nil {
		return "", err
	}
	return inj.Render(ctx, stem, vars)
}

// SetInjectRenderer installs synchronous inject rendering.
func (b *Bus) SetInjectRenderer(r *prompts.InjectRenderer) {
	if b != nil {
		b.inject = r
	}
}

// RenderInform renders an inform through this bus.
func (b *Bus) RenderInform(ctx context.Context, id ID, match MatchContext, vars map[string]any) (string, error) {
	if b == nil {
		return "", fmt.Errorf("anchor: nil bus")
	}
	return RenderInform(ctx, id, match, b.inject, vars)
}

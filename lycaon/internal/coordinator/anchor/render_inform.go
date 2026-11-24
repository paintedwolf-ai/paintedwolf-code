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
	b, err := reg.ResolveInform(id, match)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fmt.Errorf("anchor: no inform Binding for %s", id)
	}
	stem := strings.TrimSpace(b.Render)
	if stem == "" {
		return "", fmt.Errorf("anchor: empty render for Binding on %s", id)
	}
	return stem, nil
}

// ResolvedInform pairs a matched inform binding with the registry that selected it.
type ResolvedInform struct {
	Binding  *Binding
	Registry *Registry
}

// RenderHint returns OAR hint copy when the resolved binding declares hint_code.
// When isHint is true, text is the registered message or bare code ([OAR-HINT-4]),
// and found reports whether a registered hint supplied non-empty message copy.
func (r ResolvedInform) RenderHint() (text string, isHint bool, found bool) {
	if r.Binding == nil || strings.TrimSpace(r.Binding.HintCode) == "" {
		return "", false, false
	}
	code := strings.TrimSpace(r.Binding.HintCode)
	if r.Registry == nil {
		return code, true, false
	}
	text, found = r.Registry.RenderHint(code)
	return text, true, found
}

// ResolveInformTarget resolves the matching inform binding for id when its
// condition holds.
func ResolveInformTarget(ctx context.Context, id ID, match MatchContext) (ResolvedInform, bool) {
	reg := RegistryFor(ctx, match.SessionID)
	if reg == nil {
		return ResolvedInform{}, false
	}
	b, err := reg.ResolveInform(id, match)
	if err != nil || b == nil {
		return ResolvedInform{}, false
	}
	gc := oar.NewGuardContext()
	gc.Session.Phase = match.Phase
	gc.Session.Surface = match.Surface
	gc.Session.PermissionProfile = match.Profile
	gc.Session.SessionPosture = match.SessionPosture
	if !reg.WhenMatches(b, gc) {
		return ResolvedInform{}, false
	}
	return ResolvedInform{Binding: b, Registry: reg}, true
}

// InformRenderFor resolves a matching synchronous render stem.
func InformRenderFor(ctx context.Context, id ID, match MatchContext) string {
	resolved, ok := ResolveInformTarget(ctx, id, match)
	if !ok || resolved.Binding == nil {
		return ""
	}
	return strings.TrimSpace(resolved.Binding.Render)
}

// RenderInform resolves and renders an inform binding.
func RenderInform(
	ctx context.Context,
	id ID,
	match MatchContext,
	inj *prompts.InjectRenderer,
	vars map[string]any,
) (string, error) {
	reg := RegistryFor(ctx, match.SessionID)
	if reg == nil {
		return "", fmt.Errorf("anchor: Binding registry not installed")
	}
	b, err := reg.ResolveInform(id, match)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fmt.Errorf("anchor: no inform Binding for %s", id)
	}
	if strings.TrimSpace(b.HintCode) != "" {
		text, _ := reg.RenderHint(b.HintCode)
		return text, nil
	}
	if inj == nil {
		return "", fmt.Errorf("anchor: inject renderer not configured")
	}
	stem := strings.TrimSpace(b.Render)
	if stem == "" {
		return "", fmt.Errorf("anchor: empty render for Binding on %s", id)
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

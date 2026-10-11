package anchor

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bus queues catalog-backed coordinator guidance.
type Bus struct {
	kicks    *kick.KickEngine
	registry *Registry
	inject   *prompts.InjectRenderer
}

// NewBus creates an inform bus.
func NewBus(kicks *kick.KickEngine) *Bus {
	return &Bus{kicks: kicks, registry: DefaultRegistry()}
}

// SetRegistry attaches the Binding registry used for template resolution.
func (b *Bus) SetRegistry(r *Registry) {
	if b != nil {
		b.registry = r
	}
}

// Registry returns the active binding registry.
func (b *Bus) Registry() *Registry {
	if b == nil {
		return nil
	}
	if b.registry != nil {
		return b.registry
	}
	return DefaultRegistry()
}

// Kicks returns the underlying guidance queue.
func (b *Bus) Kicks() *kick.KickEngine {
	if b == nil {
		return nil
	}
	return b.kicks
}

// Emit queues an inform with its catalog surface.
func (b *Bus) Emit(ctx context.Context, sessionID string, id ID, partial Envelope) {
	b.EmitMatch(ctx, sessionID, id, partial, MatchContext{Surface: anchorcatalog.SurfaceFor(string(id))})
}

// EmitMatch queues an inform with selector facts.
func (b *Bus) EmitMatch(ctx context.Context, sessionID string, id ID, partial Envelope, match MatchContext) {
	if b == nil || b.kicks == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || id == "" {
		return
	}
	if _, ok := ParseID(string(id)); !ok {
		return
	}
	match.SessionID = sessionID
	reg := b.registryFor(ctx, sessionID)
	if reg == nil {
		return
	}
	binding, err := reg.ResolveInform(id, match)
	if err != nil {
		// A run context without its workflow version is a host defect, not
		// something the model can repair.
		slog.ErrorContext(ctx, "anchor inform unresolved", "anchor", string(id), "session_id", sessionID, "error", err)
		return
	}
	if binding == nil || binding.Render == "" {
		return
	}
	gc := oar.NewGuardContext()
	gc.Session.Phase = match.Phase
	gc.Session.Surface = match.Surface
	gc.Session.Profile = match.Profile
	gc.Session.SessionPosture = match.SessionPosture
	if !reg.WhenMatches(binding, gc) {
		return
	}
	if id == PhaseEntered {
		b.kicks.QueueDeferredLatest(sessionID, id.String(), binding.Render, partial.KickOptions()...)
		return
	}
	b.kicks.QueueDeferred(sessionID, binding.Render, partial.KickOptions()...)
}

// registryFor resolves session bindings before device bindings.
func (b *Bus) registryFor(ctx context.Context, sessionID string) *Registry {
	if r := RegistryFor(ctx, sessionID); r != nil {
		return r
	}
	return b.Registry()
}

// EmitEager stages worker data for first-prompt rendering.
func (b *Bus) EmitEager(ctx context.Context, sessionID string, id ID, data map[string]string) {
	if b == nil || b.kicks == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || id == "" {
		return
	}
	reg := b.registryFor(ctx, sessionID)
	if reg == nil {
		return
	}
	binding, err := reg.ResolveInform(id, MatchContext{SessionID: sessionID, Surface: anchorcatalog.SurfaceFor(string(id))})
	if err != nil || binding == nil || strings.TrimSpace(binding.Render) == "" {
		return
	}
	b.kicks.QueueEager(sessionID, binding.Render, data)
}

// Drop removes a queued or staged inform.
func (b *Bus) Drop(sessionID string, id ID) {
	if b == nil || b.kicks == nil {
		return
	}
	render := ""
	if reg := b.Registry(); reg != nil {
		render = reg.InformRender(id)
	}
	if render == "" {
		render = string(id)
	}
	b.kicks.DropKickID(sessionID, render)
}

// FilterSuppressedHintCodes removes hints covered by the turn's guidance.
func FilterSuppressedHintCodes(codes []string, ctx api.CoordinatorRunContext, pendingTemplatesOrAnchors ...string) []string {
	if len(codes) == 0 {
		return codes
	}
	omit := map[string]struct{}{}
	for _, pending := range pendingTemplatesOrAnchors {
		id, ok := ParseID(strings.TrimSpace(pending))
		if !ok {
			continue
		}
		policy, ok := dedupPolicy(id)
		if !ok {
			continue
		}
		for _, code := range policy.OmitHintCodes {
			if id == ComposeDone && strings.TrimSpace(ctx.CoordinatorBrief) == "" {
				continue
			}
			omit[code] = struct{}{}
		}
	}
	if len(omit) == 0 {
		return codes
	}
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if _, skip := omit[code]; skip {
			continue
		}
		out = append(out, code)
	}
	return out
}

// OmitInformWhenBoardReinjected reads the board dedup rule.
func OmitInformWhenBoardReinjected(inform ID) bool {
	policy, ok := dedupPolicy(inform)
	if !ok {
		return false
	}
	for _, c := range policy.OmitInformWhen {
		if c == "board_reinjected" {
			return true
		}
	}
	return false
}

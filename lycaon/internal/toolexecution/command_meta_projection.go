package toolexecution

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

var (
	commandEffectMutation     = mustCommandEffect(toolcontract.CommandMutation)
	commandEffectExternalExec = mustCommandEffect(toolcontract.CommandExternalExec)
	commandEffectNetwork      = mustCommandEffect(toolcontract.CommandNetworkEgress)
)

func mustCommandEffect(classes ...toolcontract.CommandEffectClass) toolcontract.CommandEffects {
	effects, err := toolcontract.NewCommandEffects(classes...)
	if err != nil {
		panic(err)
	}
	return effects
}

// ProjectCommandMeta composes the command tool description and argument schema
// from the admitted command effects.
func ProjectCommandMeta(meta tools.ToolMeta, effects toolcontract.CommandEffects, externalHidden bool) tools.ToolMeta {
	parts := []string{"base"}
	var chunks []string
	if base := strings.TrimSpace(meta.Description); base != "" {
		chunks = append(chunks, base)
	}
	appendFragment := func(key string) {
		parts = append(parts, key)
		if frag := strings.TrimSpace(meta.DescriptionEffects[key]); frag != "" {
			chunks = append(chunks, frag)
		}
	}
	if effects.Allows(commandEffectMutation) {
		appendFragment("mutation")
	}
	if effects.Allows(commandEffectExternalExec) {
		appendFragment("external_exec")
		if !externalHidden {
			appendFragment("external_exec_selector")
		}
	}
	if effects.Allows(commandEffectNetwork) {
		appendFragment("network_egress")
	}
	meta.Description = strings.TrimSpace(strings.Join(chunks, " "))
	meta.DescriptionParts = parts
	if !effects.Allows(commandEffectExternalExec) {
		meta.ArgsSchema = tools.PruneCommandReadOnlySchema(meta.ArgsSchema)
	}
	return meta
}

func (e *Metadata) commandEffectsForFilter(ctx context.Context, filter platform.ToolFilter) toolcontract.CommandEffects {
	if e == nil || e.policy == nil {
		return 0
	}
	if _, allowed, err := e.FreezeCommandEffects(ctx, filter); err == nil {
		return allowed
	}
	source, ok := e.policy.(interface {
		CommandEffects(context.Context, string) (toolcontract.CommandEffects, error)
	})
	if !ok {
		return 0
	}
	profileEffects, err := source.CommandEffects(ctx, filter.ProfileID)
	if err != nil {
		return 0
	}
	allowed, err := surfacecatalog.RestrictCommandEffects(profileEffects, filter.SurfaceID)
	if err != nil {
		return 0
	}
	return allowed
}

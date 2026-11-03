package prompts

import "strings"

// Sticky members enable inline capabilities; deferred members require activation.
var (
	surfaceCardEditTools     = []string{"restore_version"}
	surfaceCardDispatchTools = []string{"task", "delegate_dispatch"}
)

const surfaceCardMaxEnumerated = 8

// SurfaceCardVars are the facts partials/coordinator-surface-card.md renders.
type SurfaceCardVars struct {
	Label       string
	Rule        string
	MayEdit     bool
	MayRun      bool
	MayDispatch bool
	// EditDeferred distinguishes unloaded editing tools from unavailable ones.
	EditDeferred bool
	// OnlyTools enumerates sticky tools when no rule or capability summarizes them.
	OnlyTools []string
}

// An empty catalog label selects the execution-mode family during rendering.
func CoordinatorSurfaceCardVars(label, rule string, sticky, deferred []string, commandMutates bool) SurfaceCardVars {
	out := SurfaceCardVars{
		Label: strings.TrimSpace(label),
		Rule:  strings.TrimSpace(rule),
	}
	stickySet := toolNameSet(sticky)
	out.MayEdit = anyPresent(surfaceCardEditTools, stickySet) || (stickySet["command"] && commandMutates)
	out.MayRun = stickySet["verify"] || (stickySet["command"] && commandMutates)
	out.MayDispatch = anyPresent(surfaceCardDispatchTools, stickySet)
	deferredSet := toolNameSet(deferred)
	out.EditDeferred = anyPresent(surfaceCardEditTools, deferredSet) || (deferredSet["command"] && commandMutates)

	if !out.MayEdit && !out.MayRun && !out.MayDispatch && out.Rule == "" {
		if names := presentNames(sticky, stickySet); len(names) > 0 && len(names) <= surfaceCardMaxEnumerated {
			out.OnlyTools = names
		}
	}
	return out
}

func (v SurfaceCardVars) TemplateVars() map[string]any {
	return map[string]any{
		"surface_card_label": v.Label,
		"surface_card_rule":  v.Rule,
		"card_may_edit":      v.MayEdit,
		"card_may_run":       v.MayRun,
		"card_may_dispatch":  v.MayDispatch,
		"card_edit_deferred": v.EditDeferred,
		"card_only_tools":    v.OnlyTools,
	}
}

func anyPresent(names []string, set map[string]bool) bool {
	for _, name := range names {
		if set[name] {
			return true
		}
	}
	return false
}

// presentNames returns sticky names in roster order, minus the ambient tools
// every surface carries.
func presentNames(sticky []string, set map[string]bool) []string {
	out := make([]string, 0, len(sticky))
	for _, name := range sticky {
		name = strings.TrimSpace(name)
		if name == "" || name == "recall" || !set[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

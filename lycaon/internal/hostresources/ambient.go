package hostresources

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ambientLineOverhead = 16
	// AmbientBudgetRunes fits CatalogMax short id/label lines. Overflow is counted.
	AmbientBudgetRunes = CatalogMax * 48
)

// AmbientView is one prompt row. Paths and destinations stay host-side.
type AmbientView struct {
	ID       string
	Label    string
	Category string
	Status   string
	Access   string
	Guidance string
}

// AmbientPlan is the bounded projection of a snapshot onto the prompt surface.
type AmbientPlan struct {
	Resources []AmbientView
	Omitted   int
}

// AmbientInput configures one prompt-surface projection.
type AmbientInput struct {
	Snapshot Snapshot
	Surfaces []ExecutionSurface
	// MutationCapable is ProfileMutationCapable for the turn's tool profile.
	MutationCapable bool
}

// ProjectAmbient selects host-resource rows for one prompt surface.
func ProjectAmbient(in AmbientInput) AmbientPlan {
	selected := selectAmbient(in)
	sortAmbient(selected)
	return boundAmbient(selected, AmbientBudgetRunes)
}

func selectAmbient(in AmbientInput) []AmbientView {
	out := make([]AmbientView, 0, len(in.Snapshot.Resources))
	for _, state := range in.Snapshot.Resources {
		if !SurfacesIncludeAll(in.Surfaces, state.Surfaces) {
			continue
		}
		if !includeAmbient(state, in.MutationCapable) {
			continue
		}
		out = append(out, ambientView(state))
	}
	return out
}

func includeAmbient(state State, mutationCapable bool) bool {
	if mutationCapable {
		return includeWriteAmbient(state)
	}
	return includeReadAmbient(state)
}

func includeWriteAmbient(state State) bool {
	if state.Status == StatusAvailable && state.HostSupport == HostSupported {
		return true
	}
	if state.Prompt == PromptAdvertise || state.Prompt == PromptAvoid {
		return true
	}
	return state.Access == AccessAsk || state.Access == AccessDeny
}

func includeReadAmbient(state State) bool {
	return !(state.Prompt == PromptOmit && state.Access == AccessAllow)
}

func ambientView(state State) AmbientView {
	category := strings.TrimSpace(state.Category)
	if category == "" {
		category = "Other"
	}
	return AmbientView{
		ID:       state.ID,
		Label:    state.Label,
		Category: category,
		Status:   string(state.Status),
		Access:   string(state.Access),
		Guidance: string(state.Prompt),
	}
}

func sortAmbient(views []AmbientView) {
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Category != views[j].Category {
			return views[i].Category < views[j].Category
		}
		return views[i].ID < views[j].ID
	})
}

func boundAmbient(views []AmbientView, budget int) AmbientPlan {
	plan := AmbientPlan{}
	used := 0
	for i, view := range views {
		cost := ambientLineCost(view)
		if used+cost > budget {
			plan.Omitted = len(views) - i
			break
		}
		plan.Resources = append(plan.Resources, view)
		used += cost
	}
	return plan
}

func ambientLineCost(view AmbientView) int {
	return utf8.RuneCountInString(view.ID) +
		utf8.RuneCountInString(view.Label) +
		utf8.RuneCountInString(view.Category) +
		utf8.RuneCountInString(view.Status) +
		utf8.RuneCountInString(view.Access) +
		ambientLineOverhead
}

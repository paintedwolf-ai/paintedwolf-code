package hostresources

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	ambientLineOverhead = 16
	// ambientUseOverhead covers a use's separators and fixed words.
	ambientUseOverhead = 12
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
	// Uses are the resource's uses this surface can take.
	Uses []AmbientUse
}

// AmbientUse is one use of a resource a surface can take, with what it needs.
type AmbientUse struct {
	Label        string
	Capabilities []string
	Alone        bool
	Ports        []int
	// Through are the reachable resources the use can run through.
	Through []string
}

// AmbientPlan is the bounded projection of a snapshot onto the prompt surface.
type AmbientPlan struct {
	Resources []AmbientView
	Omitted   int
}

// AmbientInput configures one prompt-surface projection.
type AmbientInput struct {
	Snapshot Snapshot
	Reach    Reach
	// MutationCapable is ProfileMutationCapable for the turn's tool profile.
	MutationCapable bool
}

// Reach is what one prompt surface can do with host resources. Every field is
// fixed for the life of the surface's stable prompt.
type Reach struct {
	Surfaces []ExecutionSurface
	Role     AgentRole
	// Offered are the capability_request fields the surface's process tools accept.
	Offered []string
	// Unconfined reports a platform with no subprocess sandbox, where commands
	// run only with host_execution.
	Unconfined bool
}

// MatchingUses are the uses of state the surface can take, in catalog order.
func (r Reach) MatchingUses(state State, snapshot Snapshot) []AmbientUse {
	var out []AmbientUse
	for _, use := range state.Uses {
		if len(use.Roles) > 0 && !slices.Contains(use.Roles, r.Role) {
			continue
		}
		if !r.offersAll(use.Requires.Capabilities) {
			continue
		}
		through := reachableAlternatives(snapshot, use.Requires.AnyResource)
		if len(use.Requires.AnyResource) > 0 && len(through) == 0 {
			continue
		}
		out = append(out, AmbientUse{
			Label: use.Label, Capabilities: slices.Clone(use.Requires.Capabilities),
			Alone: use.Requires.Alone, Ports: slices.Clone(use.Requires.Ports), Through: through,
		})
	}
	return out
}

// OfferedCapabilities are the capability_request fields any of tools accepts.
func OfferedCapabilities(tools []string) []string {
	var out []string
	for _, tool := range tools {
		contract, _ := toolcontract.Lookup(tool)
		for _, field := range contract.CapabilityRequestFields() {
			if !slices.Contains(out, field) {
				out = append(out, field)
			}
		}
	}
	return out
}

func (r Reach) offersAll(capabilities []string) bool {
	for _, capability := range capabilities {
		if !slices.Contains(r.Offered, capability) {
			return false
		}
	}
	return true
}

func reachableAlternatives(snapshot Snapshot, ids []string) []string {
	var out []string
	for _, id := range ids {
		for _, state := range snapshot.Resources {
			if state.ID == id && state.Status == StatusAvailable && state.HostSupport == HostSupported && state.Access != AccessDeny {
				out = append(out, id)
			}
		}
	}
	return out
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
		if !SurfacesIncludeAll(in.Reach.Surfaces, state.Surfaces) {
			continue
		}
		if !includeAmbient(state, in.MutationCapable) {
			continue
		}
		view := ambientView(state)
		view.Uses = in.Reach.MatchingUses(state, in.Snapshot)
		out = append(out, view)
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
	cost := utf8.RuneCountInString(view.ID) +
		utf8.RuneCountInString(view.Label) +
		utf8.RuneCountInString(view.Category) +
		utf8.RuneCountInString(view.Status) +
		utf8.RuneCountInString(view.Access) +
		ambientLineOverhead
	for _, use := range view.Uses {
		cost += ambientUseOverhead + utf8.RuneCountInString(use.Label)
		for _, capability := range use.Capabilities {
			cost += utf8.RuneCountInString(capability) + 4
		}
		for _, port := range use.Ports {
			cost += len(strconv.Itoa(port)) + 6
		}
		for _, id := range use.Through {
			cost += utf8.RuneCountInString(id) + 6
		}
	}
	return cost
}

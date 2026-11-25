package hoststate

import (
	"fmt"
	"slices"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// PhaseBinding is one shipped workflow phase's coordinator surface binding as
// the production resolver derives it.
type PhaseBinding struct {
	WorkflowID string
	PhaseID    string
	Resolved   workflowdef.ResolvedSurfaceBinding
	Advance    string
	// RequiresAgent is the admission fact that refuses entering the phase in
	// Ask mode.
	RequiresAgent bool
	// AllowedAgents is the roster a non-ambient run declares.
	AllowedAgents []string
}

// ShippedPhaseBindings resolves every shipped workflow phase that binds a
// coordinator surface, keeping one phase per distinct binding.
func ShippedPhaseBindings() ([]PhaseBinding, error) {
	reg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return nil, fmt.Errorf("load workflow registry: %w", err)
	}
	manifests := reg.All()
	ids := make([]string, 0, len(manifests))
	for id := range manifests {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	seen := map[string]bool{}
	var out []PhaseBinding
	for _, id := range ids {
		m := manifests[id]
		for _, phase := range m.PhaseDefs {
			b, err := BindPhase(m, phase)
			if err != nil {
				return nil, err
			}
			if b.Resolved.CoordinatorSurface == "" || seen[b.Key()] {
				continue
			}
			seen[b.Key()] = true
			out = append(out, b)
		}
	}
	return out, nil
}

// BindPhase resolves phase's coordinator surface binding and advance policy.
func BindPhase(m workflowdef.Manifest, phase workflowdef.PhaseDef) (PhaseBinding, error) {
	resolved, err := workflowdef.ResolveSurfaceBinding(m, phase.ID, m.ID)
	if err != nil {
		return PhaseBinding{}, err
	}
	return PhaseBinding{
		WorkflowID:    m.ID,
		PhaseID:       phase.ID,
		Resolved:      resolved,
		Advance:       string(workflowdef.EffectiveAdvancePolicy(m, phase)),
		RequiresAgent: m.PhaseRequiresAgent(phase.ID),
		AllowedAgents: slices.Clone(m.AllowedAgents),
	}, nil
}

// Key identifies the turn inputs a binding sets, so phases that bind alike
// share recipients.
func (b PhaseBinding) Key() string {
	r := b.Resolved
	return fmt.Sprintf("%s|%s|%s|%t|%t|%s|%t|%s", r.CoordinatorSurface, r.SurfaceTemplate,
		strings.Join(r.ModeRefs, ","), r.WorkflowInvestigateEligible, r.ProfileResolved, b.Advance, b.RequiresAgent,
		strings.Join(b.AllowedAgents, ","))
}

// applyPhaseBinding sets the run-context fields the coordinator turn frame
// derives from a bound workflow phase.
func applyPhaseBinding(runCtx *api.CoordinatorRunContext, b PhaseBinding) {
	runCtx.WorkflowID = b.WorkflowID
	runCtx.CurrentPhase = b.PhaseID
	runCtx.PhaseCoordinatorSurface = b.Resolved.CoordinatorSurface
	runCtx.PhaseSurfaceTemplate = b.Resolved.SurfaceTemplate
	runCtx.PhaseModeRefs = slices.Clone(b.Resolved.ModeRefs)
	if b.Resolved.ProfileResolved {
		eligible := b.Resolved.WorkflowInvestigateEligible
		runCtx.WorkflowInvestigateEligible = &eligible
	}
	runCtx.AdvanceWhenGateMet = b.Advance
}

// ActiveRunStateContexts returns base as a running run, a running run with
// failed gate leaves, and a running run awaiting feedback: the run states
// that make the run-state overlay selector choose running, gates and feedback.
func ActiveRunStateContexts(base api.CoordinatorRunContext) []api.CoordinatorRunContext {
	running := base
	running.RunStatus = string(api.WorkflowRunStatusRunning)
	gates := running
	gates.FailedLeaves = []string{"fixture-leaf"}
	feedback := running
	feedback.PendingFeedback = &api.PendingFeedback{PhaseID: base.CurrentPhase}
	return []api.CoordinatorRunContext{running, gates, feedback}
}

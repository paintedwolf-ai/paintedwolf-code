package wiring

import (
	"fmt"
	"slices"
	"sort"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// phaseEdgeKind names how the host commits a phase transition and announces
// it to the coordinator with a PhaseAdvanced wake.
type phaseEdgeKind string

const (
	edgeAutoAdvance   phaseEdgeKind = "auto_advance"
	edgeHumanApproval phaseEdgeKind = "human_approval"
	edgeUserInput     phaseEdgeKind = "user_input"
	edgeChildComplete phaseEdgeKind = "child_completion"
	edgeReviewLoop    phaseEdgeKind = "review_loop"
	edgeTransition    phaseEdgeKind = "transition"
)

// Typed admission reasons a manifest can declare for holding a wake.
const (
	holdHumanApproval    = "human_approval_awaiting"
	holdHostObligation   = "host_obligation_held"
	holdPendingUserInput = "pending_user_input"
)

type phaseEdge struct {
	from, to         string
	kind             phaseEdgeKind
	transitionID     string
	actor            string
	choice           string
	decisionRequired bool
}

func (e phaseEdge) String() string {
	switch e.kind {
	case edgeReviewLoop:
		return fmt.Sprintf("%s/review_loop(decide=%t)", e.from, e.decisionRequired)
	case edgeTransition:
		return fmt.Sprintf("%s->%s/transition(%s)", e.from, e.to, e.transitionID)
	default:
		return fmt.Sprintf("%s->%s/%s", e.from, e.to, e.kind)
	}
}

// announcedPhaseEdges lists every transition of m that the host announces with
// a PhaseAdvanced wake. Entering a terminal phase completes the run, and a
// same-phase advance re-enters through its manifest re-entry kick, so neither
// announces one.
func announcedPhaseEdges(m workflowdef.Manifest) []phaseEdge {
	var edges []phaseEdge
	for _, p := range m.PhaseDefs {
		if p.Terminal {
			continue
		}
		if p.ReviewLoop != nil {
			edges = append(edges,
				phaseEdge{from: p.ID, to: p.ID, kind: edgeReviewLoop},
				phaseEdge{from: p.ID, to: p.ID, kind: edgeReviewLoop, decisionRequired: true})
		}
		if next := p.Next; next != "" && next != p.ID && !terminalPhase(m, next) {
			edge := phaseEdge{from: p.ID, to: next, kind: edgeAutoAdvance}
			switch {
			case p.HumanApproval != nil:
				edge.kind = edgeHumanApproval
			case p.InvokeWorkflow != nil:
				edge.kind = edgeChildComplete
			case p.OnEnter.RequestUserFeedback != nil:
				edge.kind = edgeUserInput
				if options := p.OnEnter.RequestUserFeedback.Options; len(options) > 0 {
					edge.choice = options[0]
				}
			}
			edges = append(edges, edge)
		}
		for _, tr := range p.Transitions {
			if tr.To == "" || terminalPhase(m, tr.To) || len(tr.Actors) == 0 {
				continue
			}
			edges = append(edges, phaseEdge{from: p.ID, to: tr.To, kind: edgeTransition, transitionID: tr.ID, actor: tr.Actors[0]})
		}
	}
	return edges
}

func terminalPhase(m workflowdef.Manifest, id string) bool {
	def, ok := m.PhaseByID(id)
	return ok && def.Terminal
}

// pathToPhase returns the edges that walk a run from start to target, shortest
// first. Review loops stay in their phase and never lead anywhere.
func pathToPhase(m workflowdef.Manifest, start, target string) ([]phaseEdge, bool) {
	if start == target {
		return nil, true
	}
	prev := map[string]phaseEdge{}
	seen := map[string]bool{start: true}
	queue := []string{start}
	edges := announcedPhaseEdges(m)
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for _, e := range edges {
			if e.from != at || e.kind == edgeReviewLoop || seen[e.to] {
				continue
			}
			seen[e.to] = true
			prev[e.to] = e
			if e.to == target {
				var path []phaseEdge
				for p := target; p != start; p = prev[p].from {
					path = append(path, prev[p])
				}
				slices.Reverse(path)
				return path, true
			}
			queue = append(queue, e.to)
		}
	}
	return nil, false
}

// declaredHolds lists the typed reasons for which the entered phase may hold
// a wake instead of opening a coordinator turn. An invoking phase hands the
// session to its child, so the child's first phase declares holds too.
func declaredHolds(catalog map[string]workflowdef.Manifest, m workflowdef.Manifest, phaseID string) map[string]bool {
	holds := map[string]bool{}
	def, ok := m.PhaseByID(phaseID)
	if !ok {
		return holds
	}
	if def.HumanApproval != nil {
		holds[holdHumanApproval] = true
	}
	if def.HasOnEnterObligations() {
		holds[holdHostObligation] = true
	}
	if def.OnEnter.RequestUserFeedback != nil {
		holds[holdPendingUserInput] = true
	}
	if spec := def.InvokeWorkflow; spec != nil {
		if child, ok := catalog[spec.WorkflowID+"@"+spec.Version]; ok && len(child.PhaseDefs) > 0 {
			for reason := range declaredHolds(catalog, child, child.PhaseDefs[0].ID) {
				holds[reason] = true
			}
		}
	}
	return holds
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

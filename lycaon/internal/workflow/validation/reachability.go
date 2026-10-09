package validation

import (
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// ValidatePhaseReachability checks next-links, orphans, and cycles.
// Choice-transition targets (transitions[].to) count as reachable.
func ValidatePhaseReachability(m workflowdef.Manifest) []api.ComposeValidationError {
	if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		return validateAmbientReachability(m)
	}
	defs := m.PhaseDefs
	if len(defs) == 0 {
		return nil
	}
	byID := make(map[string]workflowdef.PhaseDef, len(defs))
	for _, d := range defs {
		if d.ID == "" {
			continue
		}
		byID[d.ID] = d
	}
	var out []api.ComposeValidationError
	for _, d := range defs {
		if d.Next == "" {
			continue
		}
		if _, ok := byID[d.Next]; !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("phase_dangling_next"),
				fmt.Sprintf("phases[%s].next", d.ID),
				map[string]any{"phase": d.ID, "next": d.Next}))
		}
	}
	start := firstPhaseID(m)
	if start == "" {
		return out
	}
	if _, ok := byID[start]; !ok {
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("phase_unreachable"), "phases",
			map[string]any{"phase": start}))
		return out
	}
	// Cycle detection walks next: only (choice leaves may form cycles).
	reachableNext := map[string]bool{}
	cur := start
	for cur != "" {
		if reachableNext[cur] {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("phase_cycle"),
				fmt.Sprintf("phases[%s].next", cur),
				map[string]any{"phase": cur}))
			return out
		}
		reachableNext[cur] = true
		next := byID[cur].Next
		if next == cur {
			break
		}
		cur = next
	}
	reachable := workflowdef.ReachablePhaseIDs(defs)
	for id, def := range byID {
		if _, ok := reachable[id]; ok {
			continue
		}
		if def.Terminal {
			continue
		}
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("phase_unreachable"),
			fmt.Sprintf("phases[%s]", id),
			map[string]any{"phase": id}))
	}
	return out
}

func validateAmbientReachability(m workflowdef.Manifest) []api.ComposeValidationError {
	// Ambient system manifests may use a sparse graph; still reject dangling next.
	byID := map[string]struct{}{}
	for _, d := range m.PhaseDefs {
		byID[d.ID] = struct{}{}
	}
	var out []api.ComposeValidationError
	for _, d := range m.PhaseDefs {
		if n := strings.TrimSpace(d.Next); n != "" {
			if _, ok := byID[n]; !ok {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("phase_dangling_next"),
					fmt.Sprintf("phases[%s].next", d.ID),
					map[string]any{"phase": d.ID, "next": n}))
			}
		}
	}
	return out
}

func firstPhaseID(m workflowdef.Manifest) string {
	if len(m.Phases) > 0 {
		return strings.TrimSpace(m.Phases[0])
	}
	if len(m.PhaseDefs) > 0 {
		return m.PhaseDefs[0].ID
	}
	return ""
}

package runstate

import (
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// ReviewLoopDeclaredAgents is required_agents ∪ if_spawnable, first-seen order.
func ReviewLoopDeclaredAgents(rl workflowdef.ReviewLoopDef) []string {
	return DedupeReviewAgents(rl.RequiredAgents, rl.IfSpawnable)
}

// ReviewLoopFanoutExcludedAgents are reviewers later phases will delegate —
// they cannot be survey fan-out legs.
func ReviewLoopFanoutExcludedAgents(m workflowdef.Manifest) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range m.PhaseDefs {
		if p.ReviewLoop == nil {
			continue
		}
		for _, id := range ReviewLoopDeclaredAgents(*p.ReviewLoop) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// StampReviewIfSpawnable records the phase-enter roster snapshot for if_spawnable.
func StampReviewIfSpawnable(vars map[string]any, phaseID string, agents []string) map[string]any {
	phaseID = strings.TrimSpace(phaseID)
	if phaseID == "" {
		return vars
	}
	clean := make([]string, 0, len(agents))
	seen := map[string]struct{}{}
	for _, id := range agents {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	return SetHostVar(vars, "review_if_spawnable."+phaseID, clean)
}

// ReviewIfSpawnableSnapshot reads the phase-enter if_spawnable snapshot.
// stamped is false when the host has not recorded a snapshot for this phase entry.
func ReviewIfSpawnableSnapshot(vars map[string]any, phaseID string) (agents []string, stamped bool) {
	phaseID = strings.TrimSpace(phaseID)
	if vars == nil || phaseID == "" {
		return nil, false
	}
	root, _ := vars["review_if_spawnable"].(map[string]any)
	if root == nil {
		return nil, false
	}
	raw, ok := root[phaseID]
	if !ok || raw == nil {
		return nil, false
	}
	return reviewRosterFromValue(raw)
}

// StampReviewVerdict stores a terminal review_loop verdict for later kicks.
func StampReviewVerdict(vars map[string]any, evidenceKey string, verdict map[string]string) map[string]any {
	evidenceKey = strings.TrimSpace(evidenceKey)
	if evidenceKey == "" || len(verdict) == 0 {
		return vars
	}
	copied := make(map[string]any, len(verdict))
	for k, v := range verdict {
		copied[k] = v
	}
	return SetHostVar(vars, "review_verdict."+evidenceKey, copied)
}

// ReviewVerdictFromVars reads a stamped terminal review_loop verdict.
func ReviewVerdictFromVars(vars map[string]any, evidenceKey string) map[string]string {
	evidenceKey = strings.TrimSpace(evidenceKey)
	if vars == nil || evidenceKey == "" {
		return nil
	}
	root, _ := vars["review_verdict"].(map[string]any)
	if root == nil {
		return nil
	}
	raw, _ := root[evidenceKey].(map[string]any)
	if raw == nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func reviewRosterFromValue(raw any) ([]string, bool) {
	var roster []string
	switch value := raw.(type) {
	case []string:
		roster = append([]string(nil), value...)
	case []any:
		for _, item := range value {
			id, ok := item.(string)
			if !ok {
				return nil, false
			}
			roster = append(roster, id)
		}
	default:
		return nil, false
	}
	for _, id := range roster {
		if strings.TrimSpace(id) == "" {
			return nil, false
		}
	}
	return roster, true
}

// EffectiveReviewAgents uses only the roster committed at phase entry.
func EffectiveReviewAgents(phaseID string, rl workflowdef.ReviewLoopDef, vars map[string]any) ([]string, bool) {
	if len(rl.IfSpawnable) == 0 {
		return DedupeReviewAgents(rl.RequiredAgents), true
	}
	spawnable, stamped := ReviewIfSpawnableSnapshot(vars, phaseID)
	if !stamped {
		return nil, false
	}
	return DedupeReviewAgents(rl.RequiredAgents, spawnable), true
}

func DedupeReviewAgents(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range lists {
		for _, id := range list {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

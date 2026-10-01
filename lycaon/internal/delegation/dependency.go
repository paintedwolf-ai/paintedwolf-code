package delegation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ValidateLegDependencies validates an acyclic sibling-leg graph.
func ValidateLegDependencies(legs []api.Leg) error {
	byID := make(map[string]api.Leg, len(legs))
	for _, leg := range legs {
		id := strings.TrimSpace(leg.ID)
		if id == "" {
			return fmt.Errorf("leg id required for dependency graph")
		}
		if id != leg.ID {
			return fmt.Errorf("leg id %q has surrounding whitespace", leg.ID)
		}
		if _, exists := byID[id]; exists {
			return fmt.Errorf("duplicate leg id %q", id)
		}
		byID[id] = leg
	}
	for _, leg := range legs {
		seen := map[string]struct{}{}
		for _, dependency := range leg.DependsOn {
			raw := dependency
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				return fmt.Errorf("leg %q has an empty dependency", leg.ID)
			}
			if dependency != raw {
				return fmt.Errorf("leg %q dependency %q has surrounding whitespace", leg.ID, raw)
			}
			if dependency == leg.ID {
				return fmt.Errorf("leg %q depends on itself", leg.ID)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("leg %q repeats dependency %q", leg.ID, dependency)
			}
			seen[dependency] = struct{}{}
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("leg %q depends on unknown leg %q", leg.ID, dependency)
			}
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("leg dependency cycle includes %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// DependenciesComplete reports whether all upstream results are available.
func DependenciesComplete(leg api.Leg, byID map[string]api.Leg) (bool, string) {
	for _, dependency := range leg.DependsOn {
		upstream, exists := byID[dependency]
		if !exists {
			return false, "unknown dependency " + dependency
		}
		if upstream.Status != api.LegStatusComplete {
			return false, "waiting for dependency " + dependency
		}
	}
	return true, ""
}

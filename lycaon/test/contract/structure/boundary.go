package structure

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// validateBoundary gates explicit package access promises, not inferred cohesion.
func validateBoundary(graph *Graph, key string, d declaration) error {
	if err := validate(key, d); err != nil {
		return err
	}
	var entries []string
	switch d.Kind {
	case subsystem:
		entries = d.EntryPoints
	case family:
		entries = []string{d.Contract}
	default:
		return nil
	}
	allowed := map[string]bool{}
	for _, entry := range entries {
		pkg := strings.TrimPrefix(entry, module+"/")
		if !within(key, pkg) || !graph.Packages[pkg] {
			return fmt.Errorf("declaration %s: %s must name an existing production package within the declared directory", key, entry)
		}
		allowed[pkg] = true
	}
	// Families declare a shared contract, but do not forbid composition roots from importing implementations.
	if d.Kind != subsystem {
		return nil
	}
	var members []string
	for pkg := range graph.Packages {
		if within(key, pkg) && !allowed[pkg] {
			members = append(members, pkg)
		}
	}
	slices.Sort(members)
	var violations []error
	for _, member := range members {
		for _, consumer := range graph.outsideConsumers(key, member) {
			violations = append(violations, fmt.Errorf("subsystem %s: %s imports private package %s; use a declared entry point or review the boundary in %s", key, consumer, member, declarationPath))
		}
	}
	return errors.Join(violations...)
}

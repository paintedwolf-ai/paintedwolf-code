// Package catalogruntime provides the domain-neutral mechanics shared by
// catalog-backed host subsystems. Domain packages retain their schemas, merge
// policy, adapter interfaces, readiness rules, and wire projections.
package catalogruntime

import (
	"fmt"
	"strings"
)

// Item combines a stable catalog id with a domain-defined specification.
type Item[T any] struct {
	ID   string
	Spec T
}

// Layer is one catalog source in precedence order, already decoded by its domain.
type Layer[T any] struct {
	Name  string
	Items []Item[T]
}

// MergeFunc applies one incoming item to the current effective value. The bool
// reports whether an item with the same id already exists.
type MergeFunc[T any] func(current Item[T], exists bool, incoming Item[T]) (Item[T], error)

// Catalog is one structurally immutable generation in first-seen layer order.
// Replacing an id does not move it; domains whose file order carries policy can
// therefore use the catalog as their sole ordered index.
type Catalog[T any] struct {
	items []Item[T]
	byID  map[string]Item[T]
}

// Assemble transactionally combines decoded layers.
func Assemble[T any](layers []Layer[T], merge MergeFunc[T]) (*Catalog[T], error) {
	if merge == nil {
		return nil, fmt.Errorf("catalog runtime: merge function is required")
	}
	byID := make(map[string]Item[T])
	order := make([]string, 0)
	for _, layer := range layers {
		name := strings.TrimSpace(layer.Name)
		for _, incoming := range layer.Items {
			id := strings.TrimSpace(incoming.ID)
			if id == "" {
				return nil, fmt.Errorf("catalog source %q contains an empty id", name)
			}
			incoming.ID = id
			current, exists := byID[id]
			merged, err := merge(current, exists, incoming)
			if err != nil {
				return nil, fmt.Errorf("catalog source %q item %q: %w", name, id, err)
			}
			merged.ID = id
			if !exists {
				order = append(order, id)
			}
			byID[id] = merged
		}
	}
	items := make([]Item[T], 0, len(order))
	for _, id := range order {
		items = append(items, byID[id])
	}
	return &Catalog[T]{items: items, byID: byID}, nil
}

// Get returns one item by stable id.
func (c *Catalog[T]) Get(id string) (Item[T], bool) {
	if c == nil {
		var zero Item[T]
		return zero, false
	}
	item, ok := c.byID[id]
	return item, ok
}

// Items returns the generation in first-seen layer order. Specifications with
// reference-valued fields remain the domain's responsibility to clone before
// exposing them to callers.
func (c *Catalog[T]) Items() []Item[T] {
	if c == nil {
		return nil
	}
	return append([]Item[T](nil), c.items...)
}

// IDs returns ids in first-seen layer order.
func (c *Catalog[T]) IDs() []string {
	items := c.Items()
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

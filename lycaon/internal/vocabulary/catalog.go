package vocabulary

import (
	"sort"
	"strings"
)

// CatalogEntry is one row of the exported workflow vocabulary catalog.
type CatalogEntry struct {
	ID            string `json:"id"`
	Layer         string `json:"layer"`
	Domain        string `json:"domain"`
	Status        string `json:"status"`
	Parameterized bool   `json:"parameterized"`
	Kind          string `json:"kind"`
	Overlay       bool   `json:"overlay"`
}

// ExportCatalog renders the declaration catalog as sorted rows for the
// committed schemas/workflow_vocabulary.json snapshot.
func ExportCatalog() []CatalogEntry {
	rows := make([]CatalogEntry, 0, len(declarations))
	for id, d := range declarations {
		layer := "core"
		if d.Domain != "—" {
			layer = "domain"
		}
		var kind, status string
		switch d.Usage {
		case Predicate, OverlayPredicate:
			kind, status = "predicate", "shipped"
		case CompletionForm:
			kind, status = "completion_form", "shipped"
		case CatalogOnly:
			kind, status = "reserved", "catalog"
		case HostOnly:
			kind, status = "host_only", "catalog"
		}
		rows = append(rows, CatalogEntry{
			ID:            id,
			Layer:         layer,
			Domain:        d.Domain,
			Status:        status,
			Parameterized: strings.HasSuffix(id, ":*"),
			Kind:          kind,
			Overlay:       d.Usage == OverlayPredicate || d.Usage == CompletionForm,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

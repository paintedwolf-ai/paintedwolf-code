package vocabulary

import (
	"sort"
	"strings"
)

type CatalogEntry struct {
	ID            string `json:"id"`
	Layer         string `json:"layer"`
	Domain        string `json:"domain"`
	Status        string `json:"status"`
	Parameterized bool   `json:"parameterized"`
	Kind          string `json:"kind"`
	Overlay       bool   `json:"overlay"`
}

func ExportCatalog() []CatalogEntry {
	rows := make([]CatalogEntry, 0, len(declarations))
	for id, d := range declarations {
		layer := "core"
		if d.domain != "—" {
			layer = "domain"
		}
		var kind, status string
		switch d.usage {
		case predicate, overlayPredicate:
			kind, status = "predicate", "shipped"
		case completionForm:
			kind, status = "completion_form", "shipped"
		case catalogOnly:
			kind, status = "reserved", "catalog"
		case hostOnly:
			kind, status = "host_only", "catalog"
		}
		rows = append(rows, CatalogEntry{
			ID: id, Layer: layer, Domain: d.domain, Status: status, Parameterized: strings.HasSuffix(id, ":*"),
			Kind: kind, Overlay: d.usage == overlayPredicate || d.usage == completionForm,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func lookup(id string) declaration {
	id = strings.TrimSpace(id)
	if prefix, arg, ok := strings.Cut(id, ":"); ok && arg != "" {
		id = prefix + ":*"
	}
	return declarations[id]
}

func IsPredicate(id string) bool {
	d := lookup(id)
	return d.domain != "" && (d.usage == predicate || d.usage == overlayPredicate)
}

func IsOverlayLeaf(id string) bool {
	return lookup(id).usage == overlayPredicate
}

func IsCatalogOnly(id string) bool {
	return lookup(id).usage == catalogOnly
}

func IsHostOnlyCondition(id string) bool { return lookup(id).usage == hostOnly }

// CanBind checks the declaration spelling, including the wildcard for a family.
func CanBind(id string, family bool) bool {
	if family {
		id += "*"
	} else if strings.Contains(id, ":") {
		return false
	}
	d, ok := declarations[id]
	return ok && (d.usage == predicate || d.usage == overlayPredicate)
}

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
	Overlay       bool   `json:"overlay"`
}

func ExportCatalog() []CatalogEntry {
	rows := make([]CatalogEntry, 0, len(declarations))
	for id, d := range declarations {
		layer := "core"
		if d.domain != "—" {
			layer = "domain"
		}
		status := "shipped"
		if d.usage == catalogOnly || d.usage == hostOnly {
			status = "catalog"
		}
		rows = append(rows, CatalogEntry{ID: id, Layer: layer, Domain: d.domain, Status: status, Parameterized: strings.HasSuffix(id, ":*"), Overlay: d.usage == overlayPredicate || d.usage == completionForm})
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

func DomainIDs(domain, status string) []string {
	var ids []string
	for _, row := range ExportCatalog() {
		if row.Domain == domain && row.Status == status {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

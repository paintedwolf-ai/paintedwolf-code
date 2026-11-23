package vocabulary

import "strings"

func lookup(id string) Declaration {
	id = strings.TrimSpace(id)
	if prefix, arg, ok := strings.Cut(id, ":"); ok && arg != "" {
		id = prefix + ":*"
	}
	return declarations[id]
}

func IsPredicate(id string) bool {
	d := lookup(id)
	return d.Domain != "" && (d.Usage == Predicate || d.Usage == OverlayPredicate)
}

func IsOverlayLeaf(id string) bool {
	return lookup(id).Usage == OverlayPredicate
}

func IsCatalogOnly(id string) bool {
	return lookup(id).Usage == CatalogOnly
}

func IsHostOnlyCondition(id string) bool { return lookup(id).Usage == HostOnly }

// CanBind checks the declaration spelling, including the wildcard for a family.
func CanBind(id string, family bool) bool {
	if family {
		id += "*"
	} else if strings.Contains(id, ":") {
		return false
	}
	d, ok := declarations[id]
	return ok && (d.Usage == Predicate || d.Usage == OverlayPredicate)
}

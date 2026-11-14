package layeredfs

import (
	"slices"
	"strings"
	"syscall"
)

// hidden reports a path at or under engine storage its root contains.
func (rt *rootTable) hidden(qpath QualifiedPath) bool {
	spec := rt.byID[qpath.RootID]
	for _, hidden := range spec.Hidden {
		if qpath.Rel == hidden || strings.HasPrefix(qpath.Rel, hidden+"/") {
			return true
		}
	}
	if len(spec.HiddenNames) == 0 {
		return false
	}
	for name := range strings.SplitSeq(qpath.Rel, "/") {
		if slices.Contains(spec.HiddenNames, name) {
			return true
		}
	}
	return false
}

// refuseHidden stops a mutation from creating or replacing engine storage under its own name.
func (rt *rootTable) refuseHidden(qpath QualifiedPath) error {
	if rt.hidden(qpath) {
		return syscall.EACCES
	}
	return nil
}

func cleanHiddenRels(rels []string) []string {
	var out []string
	for _, rel := range rels {
		if clean := cleanRelPath(rel); clean != "." && clean != "" {
			out = append(out, clean)
		}
	}
	return out
}
